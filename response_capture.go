package servekit

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// capturedResponseWriter is Servekit's internal ResponseWriter wrapper
// contract.
//
// The wrapper records the committed status code and response body bytes while
// still forwarding the real WriteHeader/Write calls to the underlying writer.
// Status starts at 200 to mirror net/http's implicit success semantics when a
// handler writes a body without first calling WriteHeader.
//
// BytesWritten is intentionally narrow: it counts response body bytes accepted
// by the wrapped writer's normal response path, including Write and the
// io.ReaderFrom fast path when available. It does not include response
// headers, transfer-encoding framing, compression, TLS overhead, or any
// traffic written after the connection is hijacked.
//
// http.ResponseWriter itself only guarantees Header, Write, and WriteHeader.
// Extra behavior such as Flush or Hijack comes from separate optional
// interfaces implemented by the concrete writer supplied by net/http or by a
// wrapper around it.
//
// responseCapture also preserves the io.ReaderFrom fast path when the
// underlying writer supports it. That keeps io.Copy-based handlers and reverse
// proxies from losing the destination-driven copy optimization just because
// Servekit wrapped the writer.
type capturedResponseWriter interface {
	http.ResponseWriter
	StatusCode() int
	BytesWritten() int
	Committed() bool
	Unwrap() http.ResponseWriter
}

type responseCaptureHooks struct {
	trackHijack func(net.Conn) net.Conn
}

type responseCapture struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

// captureWriter wraps w once per request and preserves the optional writer
// capabilities that raw HandleHTTP handlers most commonly rely on.
//
// Flush is the streaming capability: it asks the server to push buffered
// response data toward the client sooner instead of waiting for normal buffering
// behavior. net/http's default HTTP/1.x and HTTP/2 ResponseWriters support
// Flusher, but wrappers may accidentally hide it.
//
// Hijack is the raw-connection takeover capability: after Hijack, the handler
// stops using normal ResponseWriter semantics and becomes responsible for the
// underlying connection directly. net/http's default HTTP/1.x ResponseWriter
// supports Hijacker, but HTTP/2 intentionally does not because one HTTP/2
// connection can carry multiple multiplexed streams rather than acting like one
// handler's private byte stream.
//
// Servekit preserves whichever of these capabilities the underlying writer
// already has so middleware does not break raw handlers. It does not invent
// capabilities the active server/protocol path does not provide. Unwrap is also
// exposed so http.ResponseController-based code can still reach the original
// writer when needed.
func captureWriter(w http.ResponseWriter, hooks responseCaptureHooks) capturedResponseWriter {
	if existing, ok := w.(capturedResponseWriter); ok {
		if setter, ok := existing.(interface{ setHijackTracker(func(net.Conn) net.Conn) }); ok {
			setter.setHijackTracker(hooks.trackHijack)
		}
		return existing
	}

	base := &responseCapture{ResponseWriter: w, status: http.StatusOK}
	flusher, canFlush := w.(http.Flusher)
	hijacker, canHijack := w.(http.Hijacker)

	switch {
	case canFlush && canHijack:
		return &responseCaptureFlushHijack{
			responseCapture: base,
			flusher:         flusher,
			hijacker:        hijacker,
			trackHijack:     hooks.trackHijack,
		}
	case canFlush:
		return &responseCaptureFlush{responseCapture: base, flusher: flusher}
	case canHijack:
		return &responseCaptureHijack{
			responseCapture: base,
			hijacker:        hijacker,
			trackHijack:     hooks.trackHijack,
		}
	default:
		return base
	}
}

func (w *responseCapture) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseCapture) WriteHeader(code int) {
	if w.wroteHeader {
		// Forward the redundant call to preserve net/http's own behavior for
		// callers that invoke WriteHeader more than once.
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.wroteHeader = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseCapture) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *responseCapture) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		w.bytes += int(n)
		return n, err
	}
	return io.Copy(responseCaptureWriter{capture: w}, r)
}

// StatusCode returns the HTTP status Servekit observed for the response. If
// the handler never explicitly wrote a status, this remains the implicit
// net/http default of 200.
func (w *responseCapture) StatusCode() int {
	return w.status
}

// Committed reports whether the wrapped writer has already committed response
// headers.
func (w *responseCapture) Committed() bool {
	return w.wroteHeader
}

// BytesWritten returns response body bytes accepted by the wrapped writer via
// the normal response path, including Write and ReadFrom. It excludes headers
// and wire-level overhead, so it is useful for request logging but should not
// be treated as an exact network-traffic count.
func (w *responseCapture) BytesWritten() int {
	return w.bytes
}

type responseCaptureFlush struct {
	*responseCapture
	flusher http.Flusher
}

func (w *responseCaptureFlush) Flush() {
	w.flusher.Flush()
}

type responseCaptureHijack struct {
	*responseCapture
	hijacker    http.Hijacker
	trackHijack func(net.Conn) net.Conn
}

func (w *responseCaptureHijack) setHijackTracker(fn func(net.Conn) net.Conn) {
	if fn != nil && w.trackHijack == nil {
		w.trackHijack = fn
	}
}

func (w *responseCaptureHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackWithTracking(w.hijacker, w.trackHijack)
}

type responseCaptureFlushHijack struct {
	*responseCapture
	flusher     http.Flusher
	hijacker    http.Hijacker
	trackHijack func(net.Conn) net.Conn
}

func (w *responseCaptureFlushHijack) setHijackTracker(fn func(net.Conn) net.Conn) {
	if fn != nil && w.trackHijack == nil {
		w.trackHijack = fn
	}
}

func (w *responseCaptureFlushHijack) Flush() {
	w.flusher.Flush()
}

func (w *responseCaptureFlushHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackWithTracking(w.hijacker, w.trackHijack)
}

type responseCaptureWriter struct {
	capture *responseCapture
}

func (w responseCaptureWriter) Write(p []byte) (int, error) {
	return w.capture.Write(p)
}

func hijackWithTracking(hijacker http.Hijacker, trackHijack func(net.Conn) net.Conn) (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := hijacker.Hijack()
	if err != nil || trackHijack == nil {
		return conn, rw, err
	}
	return trackHijack(conn), rw, nil
}

func completedStatusCode(w capturedResponseWriter, panicked bool) int {
	if panicked && !w.Committed() {
		return http.StatusInternalServerError
	}
	return w.StatusCode()
}

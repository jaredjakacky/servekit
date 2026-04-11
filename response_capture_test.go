package servekit_test

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

func TestHandleHTTPImplicitWriteIsLoggedWithStatusAndBytes(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	s := newResponseCaptureServer(&logs)
	s.HandleHTTP(http.MethodGet, "/implicit", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("abc"))
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/implicit")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "abc" {
		t.Fatalf("body = %q, want %q", got, "abc")
	}

	logText := logs.String()
	if !strings.Contains(logText, "status=200") {
		t.Fatalf("logs = %q, want status=200", logText)
	}
	if !strings.Contains(logText, "bytes=3") {
		t.Fatalf("logs = %q, want bytes=3", logText)
	}
}

func TestHandleHTTPExplicitStatusIsLoggedWithBytes(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	s := newResponseCaptureServer(&logs)
	s.HandleHTTP(http.MethodGet, "/teapot", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brew"))
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/teapot")

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if got := rec.Body.String(); got != "brew" {
		t.Fatalf("body = %q, want %q", got, "brew")
	}

	logText := logs.String()
	if !strings.Contains(logText, "status=418") {
		t.Fatalf("logs = %q, want status=418", logText)
	}
	if !strings.Contains(logText, "bytes=4") {
		t.Fatalf("logs = %q, want bytes=4", logText)
	}
}

func TestHandleHTTPPreservesFlusherCapability(t *testing.T) {
	t.Parallel()

	s := newResponseCaptureServer(io.Discard)
	s.HandleHTTP(http.MethodGet, "/flush", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "flusher missing", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("flushed"))
		flusher.Flush()
	}))

	rec := newFlushRecorder()
	req := httptest.NewRequest(http.MethodGet, "/flush", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "flushed" {
		t.Fatalf("body = %q, want %q", got, "flushed")
	}
	if rec.flushCalls == 0 {
		t.Fatal("Flush() was not called")
	}
}

func TestHandleHTTPPreservesHijackerCapability(t *testing.T) {
	t.Parallel()

	s := newResponseCaptureServer(io.Discard)
	s.HandleHTTP(http.MethodGet, "/hijack", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacker missing", http.StatusInternalServerError)
			return
		}

		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()

		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = rw.WriteString("Content-Type: text/plain\r\n")
		_, _ = rw.WriteString("Connection: close\r\n")
		_, _ = rw.WriteString("\r\n")
		_, _ = rw.WriteString("hijacked")
		_ = rw.Flush()
	}))

	writer := newHijackRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hijack", nil)
	s.Handler().ServeHTTP(writer, req)

	if !writer.hijacked {
		t.Fatal("Hijack() was not called")
	}
	if got := writer.hijackedOutput.String(); !strings.Contains(got, "HTTP/1.1 200 OK") || !strings.Contains(got, "hijacked") {
		t.Fatalf("hijacked output = %q, want raw HTTP response containing status line and body", got)
	}
}

func TestHandleHTTPPreservesReaderFromFastPath(t *testing.T) {
	t.Parallel()

	s := newResponseCaptureServer(io.Discard)
	s.HandleHTTP(http.MethodGet, "/copy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(io.ReaderFrom); !ok {
			http.Error(w, "readerfrom missing", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err := io.Copy(w, &copyOnlyReader{data: []byte("copied")})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))

	writer := newReaderFromRecorder()
	req := httptest.NewRequest(http.MethodGet, "/copy", nil)
	s.Handler().ServeHTTP(writer, req)

	if writer.code != http.StatusOK {
		t.Fatalf("status = %d, want %d", writer.code, http.StatusOK)
	}
	if got := writer.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", got, "text/plain; charset=utf-8")
	}
	if got := writer.body.String(); got != "copied" {
		t.Fatalf("body = %q, want %q", got, "copied")
	}
	if writer.readFromCalls != 1 {
		t.Fatalf("ReadFrom call count = %d, want 1", writer.readFromCalls)
	}
	if writer.writeCalls != 0 {
		t.Fatalf("Write call count = %d, want 0 when ReaderFrom fast path is preserved", writer.writeCalls)
	}
}

func newResponseCaptureServer(logOutput io.Writer) *servekit.Server {
	logger := slog.New(slog.NewTextHandler(logOutput, nil))
	return servekit.New(
		servekit.WithLogger(logger),
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	)
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushCalls int
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *flushRecorder) Flush() {
	r.flushCalls++
}

type hijackRecorder struct {
	header         http.Header
	hijacked       bool
	hijackedOutput bytes.Buffer
}

func newHijackRecorder() *hijackRecorder {
	return &hijackRecorder{header: make(http.Header)}
}

func (r *hijackRecorder) Header() http.Header {
	return r.header
}

func (r *hijackRecorder) WriteHeader(statusCode int) {}

func (r *hijackRecorder) Write(p []byte) (int, error) {
	return len(p), nil
}

func (r *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	r.hijacked = true
	rw := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(&r.hijackedOutput))
	return stubConn{}, rw, nil
}

type stubConn struct{}

func (stubConn) Read([]byte) (int, error)           { return 0, io.EOF }
func (stubConn) Write(p []byte) (int, error)        { return len(p), nil }
func (stubConn) Close() error                       { return nil }
func (stubConn) LocalAddr() net.Addr                { return stubAddr("local") }
func (stubConn) RemoteAddr() net.Addr               { return stubAddr("remote") }
func (stubConn) SetDeadline(t time.Time) error      { return nil }
func (stubConn) SetReadDeadline(t time.Time) error  { return nil }
func (stubConn) SetWriteDeadline(t time.Time) error { return nil }

type stubAddr string

func (a stubAddr) Network() string { return string(a) }
func (a stubAddr) String() string  { return string(a) }

type readerFromRecorder struct {
	header        http.Header
	body          bytes.Buffer
	code          int
	wroteHeader   bool
	writeCalls    int
	readFromCalls int
}

func newReaderFromRecorder() *readerFromRecorder {
	return &readerFromRecorder{header: make(http.Header)}
}

func (r *readerFromRecorder) Header() http.Header {
	return r.header
}

func (r *readerFromRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.code = statusCode
}

func (r *readerFromRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.writeCalls++
	return r.body.Write(p)
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.readFromCalls++
	return io.Copy(&r.body, src)
}

type copyOnlyReader struct {
	data []byte
	off  int
}

func (r *copyOnlyReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

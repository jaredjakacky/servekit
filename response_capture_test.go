package servekit_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestHandleEncodingFailureIsLoggedWithErrorStatusAndBytes(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	s := newResponseCaptureServer(&logs)
	s.Handle(http.MethodGet, "/bad", func(r *http.Request) (any, error) {
		return make(chan int), nil
	})

	rec := performRequest(t, s.Handler(), http.MethodGet, "/bad")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	logText := logs.String()
	if !strings.Contains(logText, "status=500") {
		t.Fatalf("logs = %q, want status=500", logText)
	}
	wantBytes := "bytes=" + strconv.Itoa(rec.Body.Len())
	if !strings.Contains(logText, wantBytes) {
		t.Fatalf("logs = %q, want %s", logText, wantBytes)
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

func TestHandleHTTPFlushBeforeWriteHeaderCommitsImplicitOK(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	s := newResponseCaptureServer(&logs)
	s.HandleHTTP(http.MethodGet, "/flush", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
	}))

	rec := newFlushRecorder()
	req := httptest.NewRequest(http.MethodGet, "/flush", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.writeHeaderCalls != 1 {
		t.Fatalf("WriteHeader call count = %d, want 1", rec.writeHeaderCalls)
	}
	if rec.flushCalls != 1 {
		t.Fatalf("Flush call count = %d, want 1", rec.flushCalls)
	}
	if got := logs.String(); !strings.Contains(got, "status=200") {
		t.Fatalf("logs = %q, want status=200", got)
	}
}

func TestRecoveryAbortsAfterFlush(t *testing.T) {
	t.Parallel()

	handler := servekit.Recovery(nil, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		panic("boom")
	}))

	rec := newFlushRecorder()
	req := httptest.NewRequest(http.MethodGet, "/flush-panic", nil)

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recovered panic = %v, want %v", recovered, http.ErrAbortHandler)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.writeHeaderCalls != 1 {
			t.Fatalf("WriteHeader call count = %d, want 1", rec.writeHeaderCalls)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("body = %q, want no recovery fallback after Flush", rec.Body.String())
		}
	}()

	handler.ServeHTTP(rec, req)
}

func TestServerAbortsCommittedPanicResponse(t *testing.T) {
	t.Parallel()

	var appLogs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&appLogs, nil))
	s := servekit.New(
		servekit.WithLogger(logger),
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithAccessLogEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	)
	s.HandleHTTP(http.MethodGet, "/panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		panic("boom")
	}))

	var serverLogs bytes.Buffer
	server := httptest.NewUnstartedServer(s.Handler())
	server.Config.ErrorLog = log.New(&serverLogs, "", 0)
	server.Start()
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/panic")
	if err != nil {
		t.Fatalf("GET committed panic route: %v", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatal("response body read error = nil, want transport interruption")
	}
	if got := string(body); got != "partial" {
		t.Fatalf("response body = %q, want %q", got, "partial")
	}
	if got := appLogs.String(); !strings.Contains(got, "panic observed") || !strings.Contains(got, "panic=boom") {
		t.Fatalf("Servekit logs = %q, want original panic", got)
	}
	if serverLogs.Len() != 0 {
		t.Fatalf("net/http server logs = %q, want none for ErrAbortHandler", serverLogs.String())
	}
}

func TestResponseControllerFlushPreservesUnderlyingError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("flush failed")
	rec := newFlushRecorder()
	rec.flushErr = wantErr

	var gotErr error
	s := newResponseCaptureServer(io.Discard)
	s.HandleHTTP(http.MethodGet, "/flush-error", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotErr = http.NewResponseController(w).Flush()
	}))

	req := httptest.NewRequest(http.MethodGet, "/flush-error", nil)
	s.Handler().ServeHTTP(rec, req)

	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("ResponseController.Flush error = %v, want %v", gotErr, wantErr)
	}
	if rec.writeHeaderCalls != 1 || rec.Code != http.StatusOK {
		t.Fatalf("WriteHeader calls/status = %d/%d, want 1/%d", rec.writeHeaderCalls, rec.Code, http.StatusOK)
	}
}

func TestHandleHTTPPreservesHijackerCapability(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	s := newResponseCaptureServer(&logs)
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
	logText := logs.String()
	if !strings.Contains(logText, "hijacked=true") {
		t.Fatalf("logs = %q, want hijacked=true", logText)
	}
	if strings.Contains(logText, "status=") {
		t.Fatalf("logs = %q, want no invented status for an unobserved raw response", logText)
	}
}

func TestRecoveryPropagatesAbortAfterSuccessfulHijack(t *testing.T) {
	t.Parallel()

	handler := servekit.Recovery(nil, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		defer conn.Close()
		panic("boom")
	}))

	writer := newHijackRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hijack-panic", nil)

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recovered panic = %v, want %v", recovered, http.ErrAbortHandler)
		}
		if !writer.hijacked {
			t.Fatal("Hijack() was not called")
		}
		if writer.writeHeaderCalls != 0 {
			t.Fatalf("WriteHeader call count = %d, want 0 after successful Hijack", writer.writeHeaderCalls)
		}
		if writer.writeCalls != 0 {
			t.Fatalf("Write call count = %d, want 0 after successful Hijack", writer.writeCalls)
		}
	}()

	handler.ServeHTTP(writer, req)
}

func TestRecoveryWritesFallbackAfterFailedHijack(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("hijack failed")
	handler := servekit.Recovery(nil, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := w.(http.Hijacker).Hijack(); !errors.Is(err, wantErr) {
			t.Fatalf("Hijack() error = %v, want %v", err, wantErr)
		}
		panic("boom")
	}))

	writer := newHijackRecorder()
	writer.hijackErr = wantErr
	req := httptest.NewRequest(http.MethodGet, "/hijack-failed", nil)
	handler.ServeHTTP(writer, req)

	if writer.hijacked {
		t.Fatal("writer marked hijacked after failed Hijack")
	}
	if writer.writeHeaderCalls != 1 {
		t.Fatalf("WriteHeader call count = %d, want recovery fallback", writer.writeHeaderCalls)
	}
	if writer.writeCalls != 1 {
		t.Fatalf("Write call count = %d, want recovery fallback", writer.writeCalls)
	}
}

func TestRecoveryCanWriteFinalErrorAfterInformationalResponse(t *testing.T) {
	t.Parallel()

	handler := servekit.Recovery(nil, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		panic("boom")
	}))

	writer := &multiStatusRecorder{header: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/early-hints-panic", nil)
	handler.ServeHTTP(writer, req)

	if len(writer.statuses) != 2 || writer.statuses[0] != http.StatusEarlyHints || writer.statuses[1] != http.StatusInternalServerError {
		t.Fatalf("statuses = %v, want [%d %d]", writer.statuses, http.StatusEarlyHints, http.StatusInternalServerError)
	}
	if !strings.Contains(writer.body.String(), "internal server error") {
		t.Fatalf("body = %q, want recovery fallback", writer.body.String())
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
	flushCalls       int
	writeHeaderCalls int
	flushErr         error
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *flushRecorder) Flush() {
	r.flushCalls++
}

func (r *flushRecorder) FlushError() error {
	r.flushCalls++
	return r.flushErr
}

func (r *flushRecorder) WriteHeader(code int) {
	r.writeHeaderCalls++
	r.ResponseRecorder.WriteHeader(code)
}

type hijackRecorder struct {
	header           http.Header
	hijacked         bool
	hijackedOutput   bytes.Buffer
	hijackErr        error
	writeHeaderCalls int
	writeCalls       int
}

func newHijackRecorder() *hijackRecorder {
	return &hijackRecorder{header: make(http.Header)}
}

func (r *hijackRecorder) Header() http.Header {
	return r.header
}

func (r *hijackRecorder) WriteHeader(statusCode int) {
	r.writeHeaderCalls++
}

func (r *hijackRecorder) Write(p []byte) (int, error) {
	r.writeCalls++
	return len(p), nil
}

func (r *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if r.hijackErr != nil {
		return nil, nil, r.hijackErr
	}
	r.hijacked = true
	rw := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("")), bufio.NewWriter(&r.hijackedOutput))
	return stubConn{}, rw, nil
}

type multiStatusRecorder struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

func (r *multiStatusRecorder) Header() http.Header { return r.header }

func (r *multiStatusRecorder) WriteHeader(code int) {
	r.statuses = append(r.statuses, code)
}

func (r *multiStatusRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
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

package servekit_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servekit "github.com/jaredjakacky/servekit"
)

func TestRequestIDMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("uses incoming request id", func(t *testing.T) {
		t.Parallel()

		handler := servekit.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, servekit.RequestIDFromContext(r.Context()))
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", "req-123")
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-Request-ID"); got != "req-123" {
			t.Fatalf("X-Request-ID header = %q, want %q", got, "req-123")
		}
		if got := rec.Body.String(); got != "req-123" {
			t.Fatalf("body = %q, want %q", got, "req-123")
		}
	})

	t.Run("generates request id when missing", func(t *testing.T) {
		t.Parallel()

		handler := servekit.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, servekit.RequestIDFromContext(r.Context()))
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.ServeHTTP(rec, req)

		got := rec.Header().Get("X-Request-ID")
		if got == "" {
			t.Fatal("X-Request-ID header = empty, want non-empty")
		}
		if rec.Body.String() != got {
			t.Fatalf("body = %q, want generated request id %q", rec.Body.String(), got)
		}
	})
}

func TestCorrelationIDMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("uses incoming correlation id", func(t *testing.T) {
		t.Parallel()

		handler := servekit.CorrelationID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, servekit.CorrelationIDFromContext(r.Context()))
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Correlation-ID", "corr-123")
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-Correlation-ID"); got != "corr-123" {
			t.Fatalf("X-Correlation-ID header = %q, want %q", got, "corr-123")
		}
		if got := rec.Body.String(); got != "corr-123" {
			t.Fatalf("body = %q, want %q", got, "corr-123")
		}
	})

	t.Run("falls back to request id when present", func(t *testing.T) {
		t.Parallel()

		handler := servekit.RequestID()(servekit.CorrelationID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, servekit.RequestIDFromContext(r.Context())+"|"+servekit.CorrelationIDFromContext(r.Context()))
		})))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", "req-123")
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("X-Correlation-ID"); got != "req-123" {
			t.Fatalf("X-Correlation-ID header = %q, want %q", got, "req-123")
		}
		if got := rec.Body.String(); got != "req-123|req-123" {
			t.Fatalf("body = %q, want %q", got, "req-123|req-123")
		}
	})
}

func TestAccessLogMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("logs completed request", func(t *testing.T) {
		t.Parallel()

		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		handler := servekit.AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "abc")
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/logged", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
		}

		logText := logs.String()
		if !strings.Contains(logText, "method=GET") || !strings.Contains(logText, "path=/logged") {
			t.Fatalf("logs = %q, want method/path fields", logText)
		}
		if !strings.Contains(logText, "status=202") || !strings.Contains(logText, "bytes=3") {
			t.Fatalf("logs = %q, want status/bytes fields", logText)
		}
	})

	t.Run("skip access log suppresses log entry", func(t *testing.T) {
		t.Parallel()

		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		handler := servekit.AccessLog(logger)(servekit.SkipAccessLog()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/skip", nil)
		handler.ServeHTTP(rec, req)

		if got := logs.String(); got != "" {
			t.Fatalf("logs = %q, want empty", got)
		}
	})

	t.Run("logs panic as 500 and re-panics original value", func(t *testing.T) {
		t.Parallel()

		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		handler := servekit.AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/panic", nil)

		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Fatalf("recovered panic = %v, want %q", recovered, "boom")
			}
			logText := logs.String()
			if !strings.Contains(logText, "path=/panic") || !strings.Contains(logText, "status=500") {
				t.Fatalf("logs = %q, want panic request logged as 500", logText)
			}
		}()

		handler.ServeHTTP(rec, req)
	})
}

func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("contain and continue writes fallback json when uncommitted", func(t *testing.T) {
		t.Parallel()

		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		handler := servekit.Recovery(logger, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/panic", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got, _ := body["error"].(string); got != "internal server error" {
			t.Fatalf("error = %q, want %q", got, "internal server error")
		}
		if _, ok := body["request_id"]; ok {
			t.Fatal("request_id present without request ID middleware, want omitted")
		}
		if got := logs.String(); !strings.Contains(got, "panic observed") || !strings.Contains(got, "panic=boom") {
			t.Fatalf("logs = %q, want panic log entry", got)
		}
	})

	t.Run("contain and continue includes request id when available", func(t *testing.T) {
		t.Parallel()

		handler := servekit.Recovery(nil, false)(servekit.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/panic", nil)
		handler.ServeHTTP(rec, req)

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		requestID, _ := body["request_id"].(string)
		if requestID == "" {
			t.Fatal("request_id = empty, want non-empty")
		}
		if got := rec.Header().Get("X-Request-ID"); got != requestID {
			t.Fatalf("X-Request-ID = %q, want %q", got, requestID)
		}
	})

	t.Run("contain and continue leaves committed response untouched", func(t *testing.T) {
		t.Parallel()

		handler := servekit.Recovery(nil, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "partial")
			panic("boom")
		}))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/panic", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
		}
		if got := rec.Body.String(); got != "partial" {
			t.Fatalf("body = %q, want %q", got, "partial")
		}
	})

	t.Run("propagate mode aborts without fallback write", func(t *testing.T) {
		t.Parallel()

		handler := servekit.Recovery(nil, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		}))

		writer := &trackingResponseWriter{header: make(http.Header)}
		req := httptest.NewRequest(http.MethodGet, "/panic", nil)

		defer func() {
			if recovered := recover(); recovered != http.ErrAbortHandler {
				t.Fatalf("recovered panic = %v, want %v", recovered, http.ErrAbortHandler)
			}
			if writer.writeHeaderCalls != 0 {
				t.Fatalf("WriteHeader call count = %d, want 0", writer.writeHeaderCalls)
			}
			if writer.writeCalls != 0 {
				t.Fatalf("Write call count = %d, want 0", writer.writeCalls)
			}
		}()

		handler.ServeHTTP(writer, req)
	})
}

type trackingResponseWriter struct {
	header           http.Header
	writeHeaderCalls int
	writeCalls       int
}

func (w *trackingResponseWriter) Header() http.Header {
	return w.header
}

func (w *trackingResponseWriter) WriteHeader(statusCode int) {
	w.writeHeaderCalls++
}

func (w *trackingResponseWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	return len(p), nil
}

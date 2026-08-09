package servekit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	servekit "github.com/jaredjakacky/servekit"
)

func TestJSONResponse(t *testing.T) {
	t.Parallel()

	t.Run("nil payload writes no content", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)

		if err := servekit.JSONResponse()(rec, req, nil); err != nil {
			t.Fatalf("JSONResponse() error = %v, want nil", err)
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
		if got := rec.Body.String(); got != "" {
			t.Fatalf("body = %q, want empty", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "" {
			t.Fatalf("Content-Type = %q, want empty", got)
		}
	})

	t.Run("non nil payload writes wrapped json", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)

		payload := map[string]any{"name": "servekit", "ok": true}
		if err := servekit.JSONResponse()(rec, req, payload); err != nil {
			t.Fatalf("JSONResponse() error = %v, want nil", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
		if got := rec.Body.String(); got != "{\"data\":{\"name\":\"servekit\",\"ok\":true}}\n" {
			t.Fatalf("body = %q, want wrapped JSON with trailing newline", got)
		}

		var body map[string]map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		data := body["data"]
		if got, _ := data["name"].(string); got != "servekit" {
			t.Fatalf("data.name = %q, want %q", got, "servekit")
		}
		if got, _ := data["ok"].(bool); !got {
			t.Fatalf("data.ok = %v, want true", got)
		}
	})

	t.Run("unsupported payload returns error without committing response", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := &responseWriteRecorder{header: make(http.Header)}

		err := servekit.JSONResponse()(rec, req, make(chan int))
		if err == nil {
			t.Fatal("JSONResponse() error = nil, want non-nil")
		}
		if len(rec.statuses) != 0 {
			t.Fatalf("committed statuses = %v, want none", rec.statuses)
		}
		if rec.writeCalls != 0 {
			t.Fatalf("Write calls = %d, want 0", rec.writeCalls)
		}
		if got := rec.body.String(); got != "" {
			t.Fatalf("body = %q, want empty", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "" {
			t.Fatalf("Content-Type = %q, want empty", got)
		}
	})
}

type responseWriteRecorder struct {
	header     http.Header
	statuses   []int
	body       bytes.Buffer
	writeCalls int
}

func (r *responseWriteRecorder) Header() http.Header { return r.header }

func (r *responseWriteRecorder) WriteHeader(code int) {
	r.statuses = append(r.statuses, code)
}

func (r *responseWriteRecorder) Write(p []byte) (int, error) {
	r.writeCalls++
	return r.body.Write(p)
}

func TestJSONError(t *testing.T) {
	t.Parallel()

	var typedNilHTTPError *servekit.HTTPError
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "http error uses explicit status and message",
			err:         servekit.Error(http.StatusTeapot, "short and stout", nil),
			wantStatus:  http.StatusTeapot,
			wantMessage: "short and stout",
		},
		{
			name:        "http error value uses explicit status and message",
			err:         servekit.HTTPError{StatusCode: http.StatusConflict, Message: "conflict"},
			wantStatus:  http.StatusConflict,
			wantMessage: "conflict",
		},
		{
			name:        "http error pointer uses explicit status and message",
			err:         &servekit.HTTPError{StatusCode: http.StatusForbidden, Message: "forbidden"},
			wantStatus:  http.StatusForbidden,
			wantMessage: "forbidden",
		},
		{
			name:        "wrapped http error value uses explicit status and message",
			err:         fmt.Errorf("request failed: %w", servekit.HTTPError{StatusCode: http.StatusGone, Message: "gone"}),
			wantStatus:  http.StatusGone,
			wantMessage: "gone",
		},
		{
			name:        "wrapped http error pointer uses explicit status and message",
			err:         fmt.Errorf("request failed: %w", &servekit.HTTPError{StatusCode: http.StatusUnprocessableEntity, Message: "unprocessable"}),
			wantStatus:  http.StatusUnprocessableEntity,
			wantMessage: "unprocessable",
		},
		{
			name: "outer value wins over wrapped pointer",
			err: servekit.HTTPError{
				StatusCode: http.StatusBadGateway,
				Message:    "outer value",
				Err:        &servekit.HTTPError{StatusCode: http.StatusNotFound, Message: "inner pointer"},
			},
			wantStatus:  http.StatusBadGateway,
			wantMessage: "outer value",
		},
		{
			name: "outer pointer wins over wrapped value",
			err: &servekit.HTTPError{
				StatusCode: http.StatusServiceUnavailable,
				Message:    "outer pointer",
				Err:        servekit.HTTPError{StatusCode: http.StatusConflict, Message: "inner value"},
			},
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "outer pointer",
		},
		{
			name:        "typed nil http error pointer fails closed",
			err:         typedNilHTTPError,
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal server error",
		},
		{
			name: "http error message hides underlying error",
			err: &servekit.HTTPError{
				StatusCode: http.StatusBadGateway,
				Message:    "upstream unavailable",
				Err:        errors.New("private upstream detail"),
			},
			wantStatus:  http.StatusBadGateway,
			wantMessage: "upstream unavailable",
		},
		{
			name:        "empty http error message uses status text",
			err:         &servekit.HTTPError{StatusCode: http.StatusForbidden},
			wantStatus:  http.StatusForbidden,
			wantMessage: "forbidden",
		},
		{
			name: "zero status leaves timeout mapping in control",
			err: servekit.HTTPError{
				Err: context.DeadlineExceeded,
			},
			wantStatus:  http.StatusGatewayTimeout,
			wantMessage: "request timed out",
		},
		{
			name: "negative status leaves body limit mapping in control",
			err: &servekit.HTTPError{
				StatusCode: -1,
				Err:        &http.MaxBytesError{Limit: 32},
			},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: "request body too large",
		},
		{
			name:        "lowest final status is accepted",
			err:         servekit.HTTPError{StatusCode: 200, Message: "explicit final status"},
			wantStatus:  200,
			wantMessage: "explicit final status",
		},
		{
			name:        "highest final status is accepted",
			err:         servekit.HTTPError{StatusCode: 599, Message: "explicit final status"},
			wantStatus:  599,
			wantMessage: "explicit final status",
		},
		{
			name:        "two digit status fails closed",
			err:         servekit.HTTPError{StatusCode: 99},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal server error",
		},
		{
			name:        "informational status fails closed",
			err:         servekit.HTTPError{StatusCode: 199},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal server error",
		},
		{
			name:        "status above http range fails closed",
			err:         servekit.HTTPError{StatusCode: 600},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal server error",
		},
		{
			name:        "nonsensical three digit status fails closed",
			err:         servekit.HTTPError{StatusCode: 999},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal server error",
		},
		{
			name:        "invalid status preserves explicit client message",
			err:         &servekit.HTTPError{StatusCode: 600, Message: "safe public message"},
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "safe public message",
		},
		{
			name:        "deadline exceeded maps to timeout",
			err:         context.DeadlineExceeded,
			wantStatus:  http.StatusGatewayTimeout,
			wantMessage: "request timed out",
		},
		{
			name:        "canceled maps to canceled message",
			err:         context.Canceled,
			wantStatus:  http.StatusGatewayTimeout,
			wantMessage: "request canceled",
		},
		{
			name:        "max bytes error maps to request too large",
			err:         &http.MaxBytesError{Limit: 32},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: "request body too large",
		},
		{
			name:        "generic error falls back to internal server error text",
			err:         errors.New("boom"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal server error",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			if err := servekit.JSONError()(rec, req, tc.err); err != nil {
				t.Fatalf("JSONError() error = %v, want nil", err)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want %q", got, "application/json")
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got, _ := body["error"].(string); got != tc.wantMessage {
				t.Fatalf("error = %q, want %q", got, tc.wantMessage)
			}
			if _, ok := body["request_id"]; ok {
				t.Fatal("request_id present without request ID middleware, want omitted")
			}
		})
	}
}

func TestJSONErrorIncludesRequestIDWhenPresent(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	var wrappedReq *http.Request
	servekit.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrappedReq = r
	})).ServeHTTP(rec, req)

	errRec := httptest.NewRecorder()
	if err := servekit.JSONError()(errRec, wrappedReq, errors.New("boom")); err != nil {
		t.Fatalf("JSONError() error = %v, want nil", err)
	}

	var body map[string]any
	if err := json.Unmarshal(errRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	requestID, _ := body["request_id"].(string)
	if requestID == "" {
		t.Fatal("request_id = empty, want non-empty")
	}
	if got := errRec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	if errRec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", errRec.Code, http.StatusInternalServerError)
	}
}

func TestJSONErrorReplacesStaleContentLengthOverHTTP(t *testing.T) {
	t.Parallel()

	writeErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Request-ID", "request-123")
		w.Header().Set("X-Correlation-ID", "correlation-123")
		w.Header().Set("Access-Control-Allow-Origin", "https://client.example")
		writeErr <- servekit.JSONError()(w, r, errors.New("boom"))
	}))
	defer server.Close()

	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("GET JSON error response: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read JSON error response: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("JSONError() error = %v, want nil", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if got := resp.Header.Get("Content-Length"); got == "1000" {
		t.Fatalf("Content-Length = %q, want stale value removed", got)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != int64(len(body)) {
		t.Fatalf("response ContentLength = %d, body length = %d", resp.ContentLength, len(body))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := resp.Header.Get("X-Request-ID"); got != "request-123" {
		t.Fatalf("X-Request-ID = %q, want %q", got, "request-123")
	}
	if got := resp.Header.Get("X-Correlation-ID"); got != "correlation-123" {
		t.Fatalf("X-Correlation-ID = %q, want %q", got, "correlation-123")
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://client.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://client.example")
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got, _ := payload["error"].(string); got != "internal server error" {
		t.Fatalf("error = %q, want %q", got, "internal server error")
	}
}

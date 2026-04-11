package servekit_test

import (
	"context"
	"encoding/json"
	"errors"
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

	t.Run("unsupported payload returns error after committing default success status", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		err := servekit.JSONResponse()(rec, req, make(chan int))
		if err == nil {
			t.Fatal("JSONResponse() error = nil, want non-nil")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.String(); got != "" {
			t.Fatalf("body = %q, want empty", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
	})
}

func TestJSONError(t *testing.T) {
	t.Parallel()

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

package servekit_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servekit "github.com/jaredjakacky/servekit"
)

func TestWithResponseEncoder(t *testing.T) {
	t.Parallel()

	t.Run("custom encoder overrides default handle success encoding", func(t *testing.T) {
		t.Parallel()

		s := newOptionsTestServer(servekit.WithResponseEncoder(func(w http.ResponseWriter, r *http.Request, payload any) error {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "custom")
			return nil
		}))
		s.Handle(http.MethodGet, "/ok", func(r *http.Request) (any, error) {
			return map[string]string{"ignored": "payload"}, nil
		})

		rec := performRequest(t, s.Handler(), http.MethodGet, "/ok")

		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/plain" {
			t.Fatalf("Content-Type = %q, want %q", got, "text/plain")
		}
		if got := rec.Body.String(); got != "custom" {
			t.Fatalf("body = %q, want %q", got, "custom")
		}
	})

	t.Run("nil encoder leaves default json response behavior in place", func(t *testing.T) {
		t.Parallel()

		s := newOptionsTestServer(servekit.WithResponseEncoder(nil))
		s.Handle(http.MethodGet, "/ok", func(r *http.Request) (any, error) {
			return map[string]string{"name": "servekit"}, nil
		})

		rec := performRequest(t, s.Handler(), http.MethodGet, "/ok")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
		assertJSONPathString(t, rec.Body.Bytes(), "data.name", "servekit")
	})
}

func TestWithErrorEncoder(t *testing.T) {
	t.Parallel()

	t.Run("custom encoder overrides default handle error encoding", func(t *testing.T) {
		t.Parallel()

		s := newOptionsTestServer(servekit.WithErrorEncoder(func(w http.ResponseWriter, r *http.Request, err error) error {
			w.Header().Set("X-Error", "custom")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, "custom error")
			return nil
		}))
		s.Handle(http.MethodGet, "/err", func(r *http.Request) (any, error) {
			return nil, errors.New("boom")
		})

		rec := performRequest(t, s.Handler(), http.MethodGet, "/err")

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
		}
		if got := rec.Header().Get("X-Error"); got != "custom" {
			t.Fatalf("X-Error = %q, want %q", got, "custom")
		}
		if got := rec.Body.String(); got != "custom error" {
			t.Fatalf("body = %q, want %q", got, "custom error")
		}
	})

	t.Run("nil encoder leaves default json error behavior in place", func(t *testing.T) {
		t.Parallel()

		s := newOptionsTestServer(servekit.WithErrorEncoder(nil))
		s.Handle(http.MethodGet, "/err", func(r *http.Request) (any, error) {
			return nil, errors.New("boom")
		})

		rec := performRequest(t, s.Handler(), http.MethodGet, "/err")

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
		assertJSONPathString(t, rec.Body.Bytes(), "error", "internal server error")
	})
}

func TestWithBuildInfoOverridesVersionEndpoint(t *testing.T) {
	t.Parallel()

	s := newOptionsTestServer(
		servekit.WithBuildInfo("v1.2.3", "abc123", "2026-04-04T00:00:00Z"),
	)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/version")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
	}
	assertJSONPathString(t, rec.Body.Bytes(), "version", "v1.2.3")
	assertJSONPathString(t, rec.Body.Bytes(), "commit", "abc123")
	assertJSONPathString(t, rec.Body.Bytes(), "date", "2026-04-04T00:00:00Z")
	assertJSONPathNonEmptyString(t, rec.Body.Bytes(), "goVersion")
}

func TestWithRequestBodyLimit(t *testing.T) {
	t.Parallel()

	t.Run("limit exceeded returns 413", func(t *testing.T) {
		t.Parallel()

		s := newOptionsTestServer(servekit.WithRequestBodyLimit(3))
		s.Handle(http.MethodPost, "/read", func(r *http.Request) (any, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			return string(body), nil
		})

		rec := performRequestWithBody(t, s.Handler(), http.MethodPost, "/read", "abcd")

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
		}
		assertJSONPathString(t, rec.Body.Bytes(), "error", "request body too large")
	})

	t.Run("negative limit disables the cap", func(t *testing.T) {
		t.Parallel()

		s := newOptionsTestServer(servekit.WithRequestBodyLimit(-1))
		s.Handle(http.MethodPost, "/read", func(r *http.Request) (any, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			return string(body), nil
		})

		rec := performRequestWithBody(t, s.Handler(), http.MethodPost, "/read", "abcd")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		assertJSONPathString(t, rec.Body.Bytes(), "data", "abcd")
	})
}

func newOptionsTestServer(opts ...servekit.Option) *servekit.Server {
	base := []servekit.Option{
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithAccessLogEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	}
	base = append(base, opts...)
	return servekit.New(base...)
}

func performRequestWithBody(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec
}

func assertJSONPathString(t *testing.T, body []byte, path, want string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}

	current := any(payload)
	for _, part := range strings.Split(path, ".") {
		next, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("path %q did not resolve to an object at %q", path, part)
		}
		current = next[part]
	}

	got, _ := current.(string)
	if got != want {
		t.Fatalf("JSON %q = %q, want %q", path, got, want)
	}
}

func assertJSONPathNonEmptyString(t *testing.T, body []byte, path string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}

	got, _ := payload[path].(string)
	if got == "" {
		t.Fatalf("JSON %q = empty, want non-empty", path)
	}
}

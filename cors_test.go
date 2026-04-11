package servekit_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servekit "github.com/jaredjakacky/servekit"
)

func TestCORSPreflightAllowed(t *testing.T) {
	t.Parallel()

	called := false
	s := newCORSTestServer(servekit.WithCORSConfig(servekit.CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{http.MethodPost},
		AllowedHeaders: []string{"Content-Type", "X-Custom"},
		MaxAge:         120,
	}))
	s.HandleHTTP(http.MethodPost, "/cors", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/cors", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-custom")
	s.Handler().ServeHTTP(rec, req)

	if called {
		t.Fatal("application handler was called for preflight request")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://example.com")
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "POST" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, "POST")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Custom" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type, X-Custom")
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "120" {
		t.Fatalf("Access-Control-Max-Age = %q, want %q", got, "120")
	}
	assertContainsAllTokens(t, rec.Header().Values("Vary"), "Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers")
}

func TestCORSPreflightRejectedForDisallowedRequest(t *testing.T) {
	t.Parallel()

	called := false
	s := newCORSTestServer(servekit.WithCORSConfig(servekit.CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{http.MethodGet},
		AllowedHeaders: []string{"Content-Type"},
	}))
	s.HandleHTTP(http.MethodGet, "/cors", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/cors", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	s.Handler().ServeHTTP(rec, req)

	if called {
		t.Fatal("application handler was called for rejected preflight request")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestCORSActualRequestAddsAllowOriginAndExposeHeaders(t *testing.T) {
	t.Parallel()

	s := newCORSTestServer(servekit.WithCORSConfig(servekit.CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		ExposedHeaders: []string{"X-Trace-ID", "X-Request-ID"},
	}))
	s.HandleHTTP(http.MethodGet, "/cors", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trace-ID", "trace")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cors", nil)
	req.Header.Set("Origin", "https://example.com")
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://example.com")
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Trace-ID, X-Request-ID" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want %q", got, "X-Trace-ID, X-Request-ID")
	}
	assertContainsAllTokens(t, rec.Header().Values("Vary"), "Origin")
}

func TestCORSWildcardDefaultsApplyToActualRequests(t *testing.T) {
	t.Parallel()

	s := newCORSTestServer(servekit.WithCORSConfig(servekit.CORSConfig{}))
	s.HandleHTTP(http.MethodGet, "/cors", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cors", nil)
	req.Header.Set("Origin", "https://any.example")
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Fatalf("Vary = %q, want empty for wildcard policy", got)
	}
}

func TestCORSCredentialsEchoOriginAndSetCredentialsHeader(t *testing.T) {
	t.Parallel()

	s := newCORSTestServer(servekit.WithCORSConfig(servekit.CORSConfig{
		AllowedOrigins:   []string{"https://example.com"},
		AllowCredentials: true,
	}))
	s.HandleHTTP(http.MethodGet, "/cors", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cors", nil)
	req.Header.Set("Origin", "https://example.com")
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://example.com")
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
	assertContainsAllTokens(t, rec.Header().Values("Vary"), "Origin")
}

func TestCORSInvalidCredentialWildcardConfigPanicsWhenBuildingHandler(t *testing.T) {
	t.Parallel()

	s := newCORSTestServer(servekit.WithCORSConfig(servekit.CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
	}))

	defer func() {
		if recovered := recover(); recovered != `servekit: CORS AllowCredentials does not allow "*" in AllowedOrigins` {
			t.Fatalf("panic = %v, want expected servekit CORS panic", recovered)
		}
	}()

	_ = s.Handler()
}

func newCORSTestServer(opts ...servekit.Option) *servekit.Server {
	base := []servekit.Option{
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithAccessLogEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	}
	base = append(base, opts...)
	return servekit.New(base...)
}

func assertContainsAllTokens(t *testing.T, values []string, want ...string) {
	t.Helper()

	joined := strings.Join(values, ",")
	for _, token := range want {
		if !strings.Contains(joined, token) {
			t.Fatalf("header values %q do not contain token %q", joined, token)
		}
	}
}

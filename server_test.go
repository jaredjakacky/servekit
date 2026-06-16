package servekit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	opskit "github.com/jaredjakacky/opskit"
	servekit "github.com/jaredjakacky/servekit"
)

func TestServerReadyDefaultsFalseAndSetReadyOverrides(t *testing.T) {
	t.Parallel()

	s := newBlackBoxServer()

	if s.Ready() {
		t.Fatal("Ready() = true, want false")
	}

	s.SetReady(true)
	if !s.Ready() {
		t.Fatal("Ready() = false after SetReady(true), want true")
	}

	s.SetReady(false)
	if s.Ready() {
		t.Fatal("Ready() = true after SetReady(false), want false")
	}
}

func TestServerHandlerDefaultProbeEndpoints(t *testing.T) {
	t.Parallel()

	s := newBlackBoxServer()

	livez := performRequest(t, s.Handler(), http.MethodGet, "/livez")
	if livez.Code != http.StatusOK {
		t.Fatalf("/livez status = %d, want %d", livez.Code, http.StatusOK)
	}
	assertJSONField(t, livez, "status", "ok")

	readyzNotReady := performRequest(t, s.Handler(), http.MethodGet, "/readyz")
	if readyzNotReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d before SetReady", readyzNotReady.Code, http.StatusServiceUnavailable)
	}
	assertJSONField(t, readyzNotReady, "status", "not_ready")

	s.SetReady(true)

	readyzReady := performRequest(t, s.Handler(), http.MethodGet, "/readyz")
	if readyzReady.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, want %d after SetReady(true)", readyzReady.Code, http.StatusOK)
	}
	assertJSONField(t, readyzReady, "status", "ready")
}

func TestServerHandlerReadyzIncludesReadinessCheckFailureReason(t *testing.T) {
	t.Parallel()

	s := newBlackBoxServer(
		servekit.WithReadinessChecks(func(context.Context) error {
			return errors.New("database unavailable")
		}),
	)
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONField(t, rec, "status", "not_ready")
	assertJSONField(t, rec, "reason", "database unavailable")
}

func TestServerHandlerReadyzIncludesOpskitReadiness(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "payments", Kind: "client"},
		Fn: func(context.Context) opskit.Status {
			return opskit.NotReadyStatus("payments unavailable")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops))
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONField(t, rec, "status", "not_ready")
	assertJSONBodyField(t, rec.Body.Bytes(), "reason", "one or more readiness components are not ready")
}

func TestServerHandlerReadyzStillRequiresServekitLifecycleReadinessWithOpskit(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONField(t, rec, "status", "not_ready")
}

func TestServerHandlerOpskitReadinessUsesConfiguredTimeout(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(ctx context.Context) opskit.Status {
			deadline, ok := ctx.Deadline()
			if !ok {
				return opskit.NotReadyStatus("readiness context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > time.Second {
				return opskit.NotReadyStatus("readiness context deadline outside expected range")
			}
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsTimeout(50*time.Millisecond)))
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertJSONField(t, rec, "status", "ready")
}

func TestServerHandlerOpskitReadinessCanDisableServekitTimeout(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(ctx context.Context) opskit.Status {
			if _, ok := ctx.Deadline(); ok {
				return opskit.NotReadyStatus("readiness context has unexpected deadline")
			}
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsTimeout(0)))
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertJSONField(t, rec, "status", "ready")
}

func TestServerHandlerOpskitAdminRoutesRequireExplicitOptIn(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/admin/components")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("/admin/components status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServerHandlerOpskitAdminRoutes(t *testing.T) {
	t.Parallel()

	var statusCalls atomic.Int32
	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			statusCalls.Add(1)
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdmin()))
	h := s.Handler()

	list := performRequest(t, h, http.MethodGet, "/admin/components")
	if list.Code != http.StatusOK {
		t.Fatalf("/admin/components status = %d, want %d", list.Code, http.StatusOK)
	}
	assertJSONNestedString(t, list.Body.Bytes(), "config", "components", 0, "component", "name")
	if got := statusCalls.Load(); got != 0 {
		t.Fatalf("/admin/components called Status %d times, want 0", got)
	}

	snapshot := performRequest(t, h, http.MethodGet, "/admin/components/config")
	if snapshot.Code != http.StatusOK {
		t.Fatalf("/admin/components/config status = %d, want %d", snapshot.Code, http.StatusOK)
	}
	assertJSONNestedString(t, snapshot.Body.Bytes(), "config", "component", "name")

	missing := performRequest(t, h, http.MethodGet, "/admin/components/missing")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("/admin/components/missing status = %d, want %d", missing.Code, http.StatusNotFound)
	}
	assertJSONBodyField(t, missing.Body.Bytes(), "error", "component not found")
}

func TestServerHandlerOpskitAdminRoutesCanUseAuthGate(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdminAuthGate(func(r *http.Request) error {
		if r.Header.Get("X-Admin-Token") == "local-dev" {
			return nil
		}
		return servekit.Error(http.StatusForbidden, "admin token required", nil)
	})))
	h := s.Handler()

	denied := performRequest(t, h, http.MethodGet, "/admin/components")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want %d", denied.Code, http.StatusForbidden)
	}
	assertJSONBodyField(t, denied.Body.Bytes(), "error", "admin token required")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/components", nil)
	req.Header.Set("X-Admin-Token", "local-dev")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("allowed status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestServerHandlerOpskitAdminSnapshotEncodingFailureReturnsInternalServerError(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opsInspectionComponent{
		name: "broken",
		inspection: opskit.Inspection{
			Details: make(chan struct{}),
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdmin()))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/admin/components/broken")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("/admin/components/broken status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	assertJSONBodyField(t, rec.Body.Bytes(), "error", "response encoding failed")
}

func TestServerHandlerHealthEndpointMountedOnlyWhenConfigured(t *testing.T) {
	t.Parallel()

	t.Run("missing handler", func(t *testing.T) {
		t.Parallel()

		s := newBlackBoxServer()
		rec := performRequest(t, s.Handler(), http.MethodGet, "/healthz")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("/healthz status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("custom handler", func(t *testing.T) {
		t.Parallel()

		s := newBlackBoxServer(
			servekit.WithHealthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Health", "custom")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("ok"))
			})),
		)

		rec := performRequest(t, s.Handler(), http.MethodGet, "/healthz")

		if rec.Code != http.StatusAccepted {
			t.Fatalf("/healthz status = %d, want %d", rec.Code, http.StatusAccepted)
		}
		if got := rec.Header().Get("X-Health"); got != "custom" {
			t.Fatalf("X-Health = %q, want %q", got, "custom")
		}
		if got := rec.Body.String(); got != "ok" {
			t.Fatalf("body = %q, want %q", got, "ok")
		}
	})
}

func TestServerHandlerDefaultEndpointsCanBeDisabled(t *testing.T) {
	t.Parallel()

	s := newBlackBoxServer(servekit.WithDefaultEndpointsEnabled(false))
	h := s.Handler()

	for _, path := range []string{"/livez", "/readyz", "/version"} {
		rec := performRequest(t, h, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestServerHandlerUsesProvidedMuxAndGlobalMiddleware(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /custom", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "hit")
		_, _ = w.Write([]byte("ok"))
	})

	s := newBlackBoxServer(
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithMux(mux),
		servekit.WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Middleware", "hit")
				next.ServeHTTP(w, r)
			})
		}),
	)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/custom")

	if rec.Code != http.StatusOK {
		t.Fatalf("/custom status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Middleware"); got != "hit" {
		t.Fatalf("X-Middleware = %q, want %q", got, "hit")
	}
	if got := rec.Header().Get("X-Handler"); got != "hit" {
		t.Fatalf("X-Handler = %q, want %q", got, "hit")
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestServerRunReturnsWrappedListenError(t *testing.T) {
	t.Parallel()

	s := newBlackBoxServer(servekit.WithAddr("bad addr"))

	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if got := err.Error(); len(got) < len("listen:") || got[:len("listen:")] != "listen:" {
		t.Fatalf("Run() error = %q, want prefix %q", got, "listen:")
	}
}

func TestServerRunMarksReadyOnStartupAndNotReadyOnShutdown(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	s := newBlackBoxServer(servekit.WithAddr(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)
	if !s.Ready() {
		t.Fatal("Ready() = false after Run startup, want true")
	}

	cancel()

	if err := waitForRunResult(t, errCh, 2*time.Second); err != nil {
		t.Fatalf("Run() error = %v, want nil on context cancellation", err)
	}
	if s.Ready() {
		t.Fatal("Ready() = true after Run shutdown, want false")
	}
}

func TestServerRunRespectsExplicitReadinessControl(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	s := newBlackBoxServer(servekit.WithAddr(addr))
	s.SetReady(false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	waitForHTTPStatus(t, "http://"+addr+"/livez", http.StatusOK, 2*time.Second)
	body := waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusServiceUnavailable, 250*time.Millisecond)
	assertJSONBodyField(t, body, "status", "not_ready")
	if s.Ready() {
		t.Fatal("Ready() = true with explicit readiness control still false, want false")
	}

	cancel()

	if err := waitForRunResult(t, errCh, 2*time.Second); err != nil {
		t.Fatalf("Run() error = %v, want nil on context cancellation", err)
	}
}

func TestServerRunAppliesDrainDelayBeforeShutdown(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	drainDelay := 150 * time.Millisecond
	s := newBlackBoxServer(
		servekit.WithAddr(addr),
		servekit.WithShutdownDrainDelay(drainDelay),
		servekit.WithShutdownTimeout(1*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)

	start := time.Now()
	cancel()

	body := waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusServiceUnavailable, 250*time.Millisecond)
	assertJSONBodyField(t, body, "status", "not_ready")

	if err := waitForRunResult(t, errCh, 2*time.Second); err != nil {
		t.Fatalf("Run() error = %v, want nil on context cancellation", err)
	}
	if elapsed := time.Since(start); elapsed < 120*time.Millisecond {
		t.Fatalf("Run() returned after %v, want drain delay to keep shutdown open for at least ~120ms", elapsed)
	}
	if s.Ready() {
		t.Fatal("Ready() = true after shutdown, want false")
	}
}

func TestHandlerWithExternalServerDoesNotAutoManageReadiness(t *testing.T) {
	s := newBlackBoxServer()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := waitForHTTPStatus(t, ts.URL+"/readyz", http.StatusServiceUnavailable, 250*time.Millisecond)
	assertJSONBodyField(t, body, "status", "not_ready")

	s.SetReady(true)

	body = waitForHTTPStatus(t, ts.URL+"/readyz", http.StatusOK, 250*time.Millisecond)
	assertJSONBodyField(t, body, "status", "ready")
}

func newBlackBoxServer(opts ...servekit.Option) *servekit.Server {
	base := []servekit.Option{
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithAccessLogEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	}
	base = append(base, opts...)
	return servekit.New(base...)
}

func performRequest(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	h.ServeHTTP(rec, req)
	return rec
}

func assertJSONField(t *testing.T, rec *httptest.ResponseRecorder, key, want string) {
	t.Helper()

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}

	assertJSONBodyField(t, rec.Body.Bytes(), key, want)
}

func assertJSONBodyField(t *testing.T, body []byte, key, want string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}

	if got, _ := payload[key].(string); got != want {
		t.Fatalf("JSON %q = %q, want %q", key, got, want)
	}
}

func assertJSONNestedString(t *testing.T, body []byte, want string, path ...any) {
	t.Helper()

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}

	current := payload
	for _, step := range path {
		switch key := step.(type) {
		case string:
			obj, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("path step %q resolved %T, want object", key, current)
			}
			current = obj[key]
		case int:
			items, ok := current.([]any)
			if !ok {
				t.Fatalf("path step %d resolved %T, want array", key, current)
			}
			if key < 0 || key >= len(items) {
				t.Fatalf("path step %d out of range for array length %d", key, len(items))
			}
			current = items[key]
		default:
			t.Fatalf("unsupported path step %T", step)
		}
	}

	got, _ := current.(string)
	if got != want {
		t.Fatalf("nested JSON value at %v = %q, want %q", path, got, want)
	}
}

type opsInspectionComponent struct {
	name       string
	inspection opskit.Inspection
}

func (c opsInspectionComponent) ComponentInfo() opskit.ComponentInfo {
	return opskit.ComponentInfo{Name: c.name, Kind: "test"}
}

func (c opsInspectionComponent) Status(context.Context) opskit.Status {
	return opskit.ReadyStatus("ready")
}

func (c opsInspectionComponent) Inspect(context.Context) (opskit.Inspection, error) {
	return c.inspection, nil
}

func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback addr: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}
	return addr
}

func waitForHTTPStatus(t *testing.T, url string, want int, timeout time.Duration) []byte {
	t.Helper()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	var lastStatus int
	var lastBody string
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else {
				lastStatus = resp.StatusCode
				lastBody = string(body)
				if resp.StatusCode == want {
					return body
				}
			}
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}

	if lastErr != nil && lastStatus == 0 {
		t.Fatalf("GET %s did not reach status %d before timeout: last error %v", url, want, lastErr)
	}
	t.Fatalf("GET %s status = %d, body = %q, want status %d", url, lastStatus, lastBody, want)
	return nil
}

func waitForRunResult(t *testing.T, errCh <-chan error, timeout time.Duration) error {
	t.Helper()

	select {
	case err := <-errCh:
		return err
	case <-time.After(timeout):
		t.Fatalf("Run() did not return within %v", timeout)
		return nil
	}
}

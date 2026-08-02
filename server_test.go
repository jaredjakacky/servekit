package servekit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

	var statusCalls atomic.Int32
	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			statusCalls.Add(1)
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONField(t, rec, "status", "not_ready")
	if got := statusCalls.Load(); got != 0 {
		t.Fatalf("Opskit readiness called %d times, want 0", got)
	}
}

func TestServerHandlerReadyzSkipsReadinessChecksWhenOpskitIsNotReady(t *testing.T) {
	t.Parallel()

	var checkCalls atomic.Int32
	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "payments", Kind: "client"},
		Fn: func(context.Context) opskit.Status {
			return opskit.NotReadyStatus("payments unavailable")
		},
	}, opskit.Required())

	s := newBlackBoxServer(
		servekit.WithOps(ops),
		servekit.WithReadinessChecks(func(context.Context) error {
			checkCalls.Add(1)
			return errors.New("legacy check failed")
		}),
	)
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONBodyField(t, rec.Body.Bytes(), "reason", "one or more readiness components are not ready")
	if got := checkCalls.Load(); got != 0 {
		t.Fatalf("readiness check called %d times, want 0", got)
	}
}

func TestServerHandlerReadyzRunsReadinessChecksAfterOpskitReady(t *testing.T) {
	t.Parallel()

	var checkCalls atomic.Int32
	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(
		servekit.WithOps(ops),
		servekit.WithReadinessChecks(func(context.Context) error {
			checkCalls.Add(1)
			return errors.New("legacy check failed")
		}),
	)
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONBodyField(t, rec.Body.Bytes(), "reason", "legacy check failed")
	if got := checkCalls.Load(); got != 1 {
		t.Fatalf("readiness check called %d times, want 1", got)
	}
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

func TestServerHandlerOpskitAdminAuthGateDoesNotEnableAdminRoutes(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdminAuthGate(func(r *http.Request) error {
		return nil
	})))

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

	s := newBlackBoxServer(servekit.WithOps(ops,
		servekit.WithOpsAdmin(),
		servekit.WithOpsAdminAuthGate(func(r *http.Request) error {
			if r.Header.Get("X-Admin-Token") == "local-dev" {
				return nil
			}
			return servekit.Error(http.StatusForbidden, "admin token required", nil)
		}),
	))
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

func TestServerHandlerOpskitAdminRoutesUseAccessLog(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	s := newBlackBoxServer(
		servekit.WithLogger(logger),
		servekit.WithAccessLogEnabled(true),
		servekit.WithOps(ops,
			servekit.WithOpsAdmin(),
			servekit.WithOpsAdminAuthGate(func(r *http.Request) error {
				if r.Header.Get("X-Admin-Token") == "local-dev" {
					return nil
				}
				return servekit.Error(http.StatusForbidden, "admin token required", nil)
			}),
		),
	)
	h := s.Handler()

	denied := performRequest(t, h, http.MethodGet, "/admin/components")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want %d", denied.Code, http.StatusForbidden)
	}

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/components", nil)
	req.Header.Set("X-Admin-Token", "local-dev")
	h.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status = %d, want %d", allowed.Code, http.StatusOK)
	}

	logText := logs.String()
	for _, want := range []string{
		"method=GET",
		"path=/admin/components",
		"status=403",
		"status=200",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs = %q, want %s", logText, want)
		}
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

func TestServerHandlerOpskitPresentationNeverExecutesActiveCapabilities(t *testing.T) {
	t.Parallel()

	component := &opsActiveCapabilityComponent{}
	ops := opskit.NewRegistry()
	ops.MustRegister(component, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdmin()))
	s.SetReady(true)
	h := s.Handler()

	for _, path := range []string{
		"/readyz",
		"/admin/components",
		"/admin/components/operational",
	} {
		rec := performRequest(t, h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, rec.Code, http.StatusOK)
		}
	}

	if got := component.statusCalls.Load(); got == 0 {
		t.Fatal("passive Status was not called")
	}
	if got := component.inspectCalls.Load(); got == 0 {
		t.Fatal("passive Inspect was not called")
	}
	if got := component.checkCalls.Load(); got != 0 {
		t.Fatalf("active Check called %d times, want 0", got)
	}
	if got := component.checkAllCalls.Load(); got != 0 {
		t.Fatalf("active CheckAll called %d times, want 0", got)
	}
	if got := component.checkDescriptorCalls.Load(); got != 0 {
		t.Fatalf("Checks descriptor method called %d times, want 0", got)
	}
	if got := component.commandCalls.Load(); got != 0 {
		t.Fatalf("active HandleCommand called %d times, want 0", got)
	}
	if got := component.commandDescriptorCalls.Load(); got != 0 {
		t.Fatalf("Commands descriptor method called %d times, want 0", got)
	}
}

func TestServerHandlerOpskitAdminSnapshotHonorsConfiguredTimeout(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opsBlockingStatusComponent{}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(
		ops,
		servekit.WithOpsAdmin(),
		servekit.WithOpsTimeout(10*time.Millisecond),
	))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/admin/components/slow")

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("/admin/components/slow status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
	assertJSONBodyField(t, rec.Body.Bytes(), "error", "request timed out")
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

func TestServerHandlerOpskitAdminRoutesCanBeEnabledWhenDefaultEndpointsDisabled(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithOps(ops, servekit.WithOpsAdmin()),
	)
	h := s.Handler()

	for _, path := range []string{"/livez", "/readyz", "/version"} {
		rec := performRequest(t, h, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}

	list := performRequest(t, h, http.MethodGet, "/admin/components")
	if list.Code != http.StatusOK {
		t.Fatalf("/admin/components status = %d, want %d", list.Code, http.StatusOK)
	}
	assertJSONNestedString(t, list.Body.Bytes(), "config", "components", 0, "component", "name")

	snapshot := performRequest(t, h, http.MethodGet, "/admin/components/config")
	if snapshot.Code != http.StatusOK {
		t.Fatalf("/admin/components/config status = %d, want %d", snapshot.Code, http.StatusOK)
	}
	assertJSONNestedString(t, snapshot.Body.Bytes(), "config", "component", "name")
}

func TestServerHandlerOpskitAdminRoutesStillRequireOptInWhenDefaultEndpointsDisabled(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())

	s := newBlackBoxServer(
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithOps(ops),
	)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/admin/components")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("/admin/components status = %d, want %d", rec.Code, http.StatusNotFound)
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

type opsActiveCapabilityComponent struct {
	statusCalls            atomic.Int32
	inspectCalls           atomic.Int32
	checkCalls             atomic.Int32
	checkAllCalls          atomic.Int32
	checkDescriptorCalls   atomic.Int32
	commandCalls           atomic.Int32
	commandDescriptorCalls atomic.Int32
}

func (*opsActiveCapabilityComponent) ComponentInfo() opskit.ComponentInfo {
	return opskit.ComponentInfo{Name: "operational", Kind: "test"}
}

func (c *opsActiveCapabilityComponent) Status(context.Context) opskit.Status {
	c.statusCalls.Add(1)
	return opskit.ReadyStatus("ready")
}

func (c *opsActiveCapabilityComponent) Inspect(context.Context) (opskit.Inspection, error) {
	c.inspectCalls.Add(1)
	return opskit.Inspection{Summary: "safe inspection"}, nil
}

func (c *opsActiveCapabilityComponent) Check(context.Context) opskit.CheckResult {
	c.checkCalls.Add(1)
	return opskit.ReadyCheck("check ran", 0)
}

func (c *opsActiveCapabilityComponent) CheckAll(context.Context) opskit.CheckSummary {
	c.checkAllCalls.Add(1)
	return opskit.CheckSummary{State: opskit.StateReady, Ready: true}
}

func (c *opsActiveCapabilityComponent) Checks(context.Context) []opskit.CheckDescriptor {
	c.checkDescriptorCalls.Add(1)
	return []opskit.CheckDescriptor{{Name: "active-check"}}
}

func (c *opsActiveCapabilityComponent) HandleCommand(context.Context, opskit.CommandRequest) opskit.CommandResult {
	c.commandCalls.Add(1)
	return opskit.RejectedCommand("command should not run")
}

func (c *opsActiveCapabilityComponent) Commands(context.Context) []opskit.CommandDescriptor {
	c.commandDescriptorCalls.Add(1)
	return []opskit.CommandDescriptor{{Name: "active-command"}}
}

type opsBlockingStatusComponent struct{}

func (opsBlockingStatusComponent) ComponentInfo() opskit.ComponentInfo {
	return opskit.ComponentInfo{Name: "slow", Kind: "test"}
}

func (opsBlockingStatusComponent) Status(ctx context.Context) opskit.Status {
	<-ctx.Done()
	return opskit.UnknownStatus("status evaluation canceled")
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

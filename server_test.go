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
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
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

func TestServerHandlerReadyzRedactsReadinessCheckFailure(t *testing.T) {
	t.Parallel()

	const failure = "dial tcp db.internal.example:5432: authentication failed for user payments"
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := newBlackBoxServer(
		servekit.WithLogger(logger),
		servekit.WithReadinessChecks(func(context.Context) error {
			return errors.New(failure)
		}),
	)
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	assertJSONField(t, rec, "status", "not_ready")
	assertJSONField(t, rec, "reason", "one or more readiness checks failed")
	if strings.Contains(rec.Body.String(), "db.internal.example") || strings.Contains(rec.Body.String(), "payments") {
		t.Fatalf("/readyz body = %s, want no readiness check details", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "readiness check failed") || !strings.Contains(logs.String(), failure) {
		t.Fatalf("debug logs = %q, want readiness failure details", logs.String())
	}
	if !strings.Contains(logs.String(), "check_index=0") {
		t.Fatalf("debug logs = %q, want failing readiness check index", logs.String())
	}
}

func TestServerHandlerReadyzOmitsOpskitComponentDetails(t *testing.T) {
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
	assertJSONBodyField(t, rec.Body.Bytes(), "reason", "one or more required readiness components are not ready")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz body: %v", err)
	}
	if _, ok := body["readiness"]; ok {
		t.Fatalf("/readyz body = %s, want no detailed Opskit readiness object", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "payments") || strings.Contains(rec.Body.String(), "payments unavailable") {
		t.Fatalf("/readyz body = %s, want no component identity or message", rec.Body.String())
	}
}

func TestServerHandlerReadyzHonorsOptionalOpskitParentWithBlockingChild(t *testing.T) {
	t.Parallel()

	ops := opskit.NewRegistry()
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{Name: "config", Kind: "config"},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("configuration loaded")
		},
	}, opskit.Required())
	ops.MustRegister(opsReadinessComponent{
		info:   opskit.ComponentInfo{Name: "clients", Kind: "client_registry"},
		status: opskit.NotReadyStatus("client registry not ready"),
		readiness: opskit.NotReadyReadiness("required client unavailable", opskit.ReadinessItem{
			Name:   "payments",
			Impact: opskit.ReadinessImpactBlocking,
			Ready:  false,
			State:  opskit.StateNotReady,
		}),
	}, opskit.Optional())

	s := newBlackBoxServer(servekit.WithOps(ops))
	s.SetReady(true)

	rec := performRequest(t, s.Handler(), http.MethodGet, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertJSONField(t, rec, "status", "ready")
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
	assertJSONBodyField(t, rec.Body.Bytes(), "reason", "one or more required readiness components are not ready")
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
	assertJSONBodyField(t, rec.Body.Bytes(), "reason", "one or more readiness checks failed")
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
	var snapshotBody struct {
		Registration struct {
			ReadinessPolicy string `json:"readiness_policy"`
		} `json:"registration"`
		Readiness struct {
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
			Items  []struct {
				Name   string `json:"name"`
				Impact string `json:"impact"`
				Ready  bool   `json:"ready"`
			} `json:"items"`
		} `json:"readiness"`
	}
	if err := json.Unmarshal(snapshot.Body.Bytes(), &snapshotBody); err != nil {
		t.Fatalf("decode /admin/components/config: %v", err)
	}
	if snapshotBody.Registration.ReadinessPolicy != "required" {
		t.Fatalf("snapshot registration policy = %q, want required", snapshotBody.Registration.ReadinessPolicy)
	}
	if !snapshotBody.Readiness.Ready || snapshotBody.Readiness.Reason != "component ready" {
		t.Fatalf("snapshot readiness = %+v, want ready component", snapshotBody.Readiness)
	}
	if len(snapshotBody.Readiness.Items) != 1 {
		t.Fatalf("snapshot readiness item count = %d, want 1", len(snapshotBody.Readiness.Items))
	}
	item := snapshotBody.Readiness.Items[0]
	if item.Name != "config" || item.Impact != "blocking" || !item.Ready {
		t.Fatalf("snapshot readiness item = %+v, want ready blocking config item", item)
	}

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
			return &servekit.HTTPError{
				StatusCode: http.StatusForbidden,
				Message:    "admin token required",
			}
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

func TestServerHandlerOpskitAdminSnapshotDoesNotExposeInspectorError(t *testing.T) {
	t.Parallel()

	const secret = "postgres://admin:secret@internal/config"
	ops := opskit.NewRegistry()
	ops.MustRegister(opsInspectionComponent{
		name: "broken",
		err:  errors.New("inspect failed for " + secret),
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdmin()))
	rec := performRequest(t, s.Handler(), http.MethodGet, "/admin/components/broken")

	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/components/broken status = %d, want %d", rec.Code, http.StatusOK)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("admin snapshot exposed inspector error: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"inspection_error"`) {
		t.Fatalf("admin snapshot contains legacy inspection_error: %s", rec.Body.String())
	}
	assertJSONNestedString(t, rec.Body.Bytes(), "inspection_failed", "inspection_failure", "code")
	assertJSONNestedString(t, rec.Body.Bytes(), "component inspection unavailable", "inspection_failure", "message")
}

func TestServerHandlerOpskitAdminSnapshotDoesNotExposeInspectorPanic(t *testing.T) {
	t.Parallel()

	const secret = "postgres://admin:secret@internal/config"
	ops := opskit.NewRegistry()
	ops.MustRegister(opsInspectionComponent{
		name:       "broken",
		panicValue: "inspect panicked for " + secret,
	}, opskit.Required())

	s := newBlackBoxServer(servekit.WithOps(ops, servekit.WithOpsAdmin()))
	rec := performRequest(t, s.Handler(), http.MethodGet, "/admin/components/broken")

	if rec.Code != http.StatusOK {
		t.Fatalf("/admin/components/broken status = %d, want %d", rec.Code, http.StatusOK)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("admin snapshot exposed inspector panic: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"inspection_error"`) {
		t.Fatalf("admin snapshot contains legacy inspection_error: %s", rec.Body.String())
	}
	assertJSONNestedString(t, rec.Body.Bytes(), "inspection_failed", "inspection_failure", "code")
	assertJSONNestedString(t, rec.Body.Bytes(), "component inspection unavailable", "inspection_failure", "message")
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
	s.SetReady(true)

	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if got := err.Error(); len(got) < len("listen:") || got[:len("listen:")] != "listen:" {
		t.Fatalf("Run() error = %q, want prefix %q", got, "listen:")
	}
	if s.Ready() {
		t.Fatal("Ready() = true after listen failure, want false")
	}
}

func TestServerRunWaitsForActiveRequestDuringGracefulShutdown(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	s := newBlackBoxServer(
		servekit.WithAddr(addr),
		servekit.WithShutdownTimeout(2*time.Second),
	)
	s.HandleHTTP(http.MethodGet, "/work", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- s.Run(ctx) }()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)
	requestErrCh := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/work")
		if err == nil {
			_ = resp.Body.Close()
		}
		requestErrCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("active request did not reach handler")
	}
	cancel()

	select {
	case err := <-runErrCh:
		t.Fatalf("Run() returned before active request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	released = true
	if err := <-requestErrCh; err != nil {
		t.Fatalf("active request error = %v, want graceful completion", err)
	}
	if err := waitForRunResult(t, runErrCh, 2*time.Second); err != nil {
		t.Fatalf("Run() error = %v, want nil after graceful completion", err)
	}
	if s.Ready() {
		t.Fatal("Ready() = true after graceful shutdown, want false")
	}
}

func TestServerRunForceClosesConnectionAfterShutdownTimeout(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	s := newBlackBoxServer(
		servekit.WithAddr(addr),
		servekit.WithShutdownTimeout(50*time.Millisecond),
	)
	s.HandleHTTP(http.MethodGet, "/stuck", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "too late")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- s.Run(ctx) }()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)
	requestErrCh := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
		requestErrCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stuck request did not reach handler")
	}
	cancel()

	runErr := waitForRunResult(t, runErrCh, 2*time.Second)
	if !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context deadline exceeded", runErr)
	}
	if s.Ready() {
		t.Fatal("Ready() = true after timed-out shutdown, want false")
	}
	if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("new connection succeeded after Run returned")
	}

	select {
	case err := <-requestErrCh:
		if err == nil {
			t.Fatal("active request completed without a transport error after forced close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active request connection remained open after Run returned")
	}

	close(release)
	released = true
}

func TestServerRunStopsOnSIGTERM(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestServerRunSIGTERMHelper$")
	cmd.Env = append(os.Environ(), "SERVEKIT_SIGTERM_HELPER=1", "SERVEKIT_SIGTERM_ADDR="+addr)
	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start SIGTERM helper: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	processDone := false
	defer func() {
		if !processDone {
			_ = cmd.Process.Kill()
			<-waitCh
		}
	}()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM helper: %v", err)
	}

	select {
	case err := <-waitCh:
		processDone = true
		if err != nil {
			t.Fatalf("SIGTERM helper error = %v, output: %s", err, output.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("SIGTERM helper did not exit, output: %s", output.String())
	}
}

func TestServerRunSIGTERMHelper(t *testing.T) {
	if os.Getenv("SERVEKIT_SIGTERM_HELPER") != "1" {
		return
	}

	s := newBlackBoxServer(servekit.WithAddr(os.Getenv("SERVEKIT_SIGTERM_ADDR")))
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run() error after SIGTERM = %v", err)
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

func TestServerRunWithShutdownContextBoundsDrainDelay(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	s := newBlackBoxServer(
		servekit.WithAddr(addr),
		servekit.WithShutdownDrainDelay(2*time.Second),
		servekit.WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var factoryCalls atomic.Int32
	var factoryObservedNotReady atomic.Bool
	var cancelShutdown context.CancelFunc
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.RunWithShutdownContext(ctx, func() context.Context {
			factoryCalls.Add(1)
			factoryObservedNotReady.Store(!s.Ready())
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			cancelShutdown = shutdownCancel
			return shutdownCtx
		})
	}()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)
	startedAt := time.Now()
	cancel()

	err := waitForRunResult(t, errCh, 2*time.Second)
	if cancelShutdown != nil {
		cancelShutdown()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunWithShutdownContext() error = %v, want context deadline exceeded", err)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("shutdown context factory calls = %d, want 1", got)
	}
	if !factoryObservedNotReady.Load() {
		t.Fatal("shutdown context factory observed Ready() = true, want false")
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("RunWithShutdownContext() returned after %v, want outer context to bound drain delay", elapsed)
	}
}

func TestServerRunWithShutdownContextBoundsHTTPShutdown(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	s := newBlackBoxServer(
		servekit.WithAddr(addr),
		servekit.WithShutdownTimeout(2*time.Second),
	)
	s.HandleHTTP(http.MethodGet, "/stuck-coordinated", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "too late")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelShutdown context.CancelFunc
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.RunWithShutdownContext(ctx, func() context.Context {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			cancelShutdown = shutdownCancel
			return shutdownCtx
		})
	}()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)
	requestErrCh := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/stuck-coordinated")
		if err == nil {
			_ = resp.Body.Close()
		}
		requestErrCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stuck coordinated request did not reach handler")
	}
	startedAt := time.Now()
	cancel()

	runErr := waitForRunResult(t, errCh, 2*time.Second)
	if cancelShutdown != nil {
		cancelShutdown()
	}
	if !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("RunWithShutdownContext() error = %v, want context deadline exceeded", runErr)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("RunWithShutdownContext() returned after %v, want outer context to bound HTTP shutdown", elapsed)
	}
	select {
	case err := <-requestErrCh:
		if err == nil {
			t.Fatal("active request completed without a transport error after coordinated forced close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active request connection remained open after coordinated shutdown")
	}

	close(release)
	released = true
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
	err        error
	panicValue any
}

type opsReadinessComponent struct {
	info      opskit.ComponentInfo
	status    opskit.Status
	readiness opskit.Readiness
}

func (c opsReadinessComponent) ComponentInfo() opskit.ComponentInfo {
	return c.info
}

func (c opsReadinessComponent) Status(context.Context) opskit.Status {
	return c.status
}

func (c opsReadinessComponent) Readiness(context.Context) opskit.Readiness {
	return c.readiness
}

func (c opsInspectionComponent) ComponentInfo() opskit.ComponentInfo {
	return opskit.ComponentInfo{Name: c.name, Kind: "test"}
}

func (c opsInspectionComponent) Status(context.Context) opskit.Status {
	return opskit.ReadyStatus("ready")
}

func (c opsInspectionComponent) Inspect(context.Context) (opskit.Inspection, error) {
	if c.panicValue != nil {
		panic(c.panicValue)
	}
	return c.inspection, c.err
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

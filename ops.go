package servekit

import (
	"net/http"
	"time"

	opskit "github.com/jaredjakacky/opskit"
)

const defaultOpsTimeout = 2 * time.Second

type opsConfig struct {
	adminEnabled  bool
	adminAuthGate func(*http.Request) error
	timeout       time.Duration
}

// OpsOption configures Servekit's Opskit presentation.
type OpsOption func(*opsConfig)

// WithOps wires an Opskit registry into Servekit's built-in operational routes.
//
// When registry is non-nil, /readyz includes registry readiness in the service
// readiness decision after Servekit's own lifecycle readiness is true. Admin
// component routes are not exposed unless WithOpsAdmin is supplied.
func WithOps(registry *opskit.Registry, opts ...OpsOption) Option {
	return func(s *Server) {
		if registry == nil {
			return
		}
		s.opsRegistry = registry
		for _, opt := range opts {
			if opt != nil {
				opt(&s.opsConfig)
			}
		}
	}
}

// WithOpsAdmin exposes read-only Opskit component admin routes.
//
// The routes are:
//   - GET /admin/components
//   - GET /admin/components/{name}
//
// These routes present passive registry inventory and component snapshots. They
// do not run checks, dispatch commands, or execute other active Opskit
// capabilities.
func WithOpsAdmin() OpsOption {
	return func(cfg *opsConfig) {
		cfg.adminEnabled = true
	}
}

// WithOpsAdminAuthGate installs an authorization gate for Opskit admin routes.
//
// Admin routes are only exposed when WithOpsAdmin is also supplied. Return nil
// from the gate to allow the request or a Servekit HTTPError value or pointer
// to control the denial response. A nil gate function is ignored.
func WithOpsAdminAuthGate(fn func(*http.Request) error) OpsOption {
	return func(cfg *opsConfig) {
		if fn != nil {
			cfg.adminAuthGate = fn
		}
	}
}

// WithOpsTimeout sets the timeout used for Opskit readiness and component
// snapshot reads.
//
// The default is 2 seconds. Use zero or a negative duration to disable the
// Servekit-added timeout and rely only on the incoming request context.
func WithOpsTimeout(timeout time.Duration) OpsOption {
	return func(cfg *opsConfig) {
		cfg.timeout = timeout
	}
}

package servekit

import (
	"context"
	"errors"
	"net/http"

	opskit "github.com/jaredjakacky/opskit"
)

func (s *Server) writeOpsReadinessFailure(w http.ResponseWriter, r *http.Request) bool {
	if s.opsRegistry == nil {
		return false
	}

	ctx, cancel := s.opsRequestContext(r)
	defer cancel()

	readiness := s.opsRegistry.Readiness(ctx)
	if readiness.Ready {
		return false
	}

	writeStatusJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status":    "not_ready",
		"reason":    readiness.Reason,
		"readiness": readiness,
	})
	return true
}

func (s *Server) registerOpsAdminRoutes() {
	if s.opsRegistry == nil || !s.opsConfig.adminEnabled {
		return
	}

	adminOptions := []EndpointOption{WithSkipTelemetry()}
	if s.opsConfig.adminAuthGate != nil {
		adminOptions = append(adminOptions, WithAuthGate(s.opsConfig.adminAuthGate))
	}

	s.HandleHTTP(http.MethodGet, "/admin/components", http.HandlerFunc(s.handleOpsComponents), adminOptions...)
	s.HandleHTTP(http.MethodGet, "/admin/components/{name}", http.HandlerFunc(s.handleOpsComponentSnapshot), adminOptions...)
}

func (s *Server) handleOpsComponents(w http.ResponseWriter, r *http.Request) {
	writeStatusJSON(w, http.StatusOK, map[string]any{"components": s.opsRegistry.Entries()})
}

func (s *Server) handleOpsComponentSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.opsRequestContext(r)
	defer cancel()

	snapshot, err := s.opsRegistry.Snapshot(ctx, r.PathValue("name"))
	if err != nil {
		if errors.Is(err, opskit.ErrComponentNotFound) {
			writeStatusJSON(w, http.StatusNotFound, map[string]any{"error": "component not found"})
			return
		}
		status := statusFromError(err)
		writeStatusJSON(w, status, map[string]any{"error": clientErrorMessage(err, status)})
		return
	}

	writeStatusJSON(w, http.StatusOK, snapshot)
}

func (s *Server) opsRequestContext(r *http.Request) (context.Context, context.CancelFunc) {
	if s.opsConfig.timeout <= 0 {
		return r.Context(), func() {}
	}
	return context.WithTimeout(r.Context(), s.opsConfig.timeout)
}

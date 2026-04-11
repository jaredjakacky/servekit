package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The endpoint-controls example focuses on per-route policy. Read it when a
// service mostly wants Servekit's defaults but a few routes need their own
// auth, timeout, body-limit, middleware, or observability behavior.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// A small global middleware shows how application-owned cross-cutting
	// behavior can layer onto the built-in Servekit baseline.
	s := servekit.New(
		servekit.WithAddr(":8091"),
		servekit.WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Global-Middleware", "endpoint-controls")
				next.ServeHTTP(w, r)
			})
		}),
	)

	// WithAuthCheck is the simple route-level gate: allow or deny with HTTP 401.
	s.Handle(http.MethodGet, "/admin/ping", func(r *http.Request) (any, error) {
		return map[string]string{"status": "ok"}, nil
	}, servekit.WithAuthCheck(func(r *http.Request) bool {
		return r.Header.Get("X-Admin-Token") != ""
	}))

	// WithAuthGate is the richer route-level gate: it can return a specific
	// Servekit error, while local middleware can still adjust the route only.
	s.Handle(http.MethodPost, "/admin/publish", func(r *http.Request) (any, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"status": "published",
			"bytes":  len(body),
		}, nil
	},
		servekit.WithEndpointMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Route-Scope", "admin")
				next.ServeHTTP(w, r)
			})
		}),
		servekit.WithAuthGate(func(r *http.Request) error {
			if r.Header.Get("X-Admin-Token") == "local-dev" {
				return nil
			}
			return servekit.Error(http.StatusForbidden, "admin token required", nil)
		}),
	)

	// Body limits are often route-specific rather than global policy.
	s.Handle(http.MethodPost, "/uploads/avatar", func(r *http.Request) (any, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"status": "stored",
			"bytes":  len(body),
		}, nil
	}, servekit.WithBodyLimit(64<<10))

	// Route-level timeouts change the request context seen by the handler
	// without forcing a server-wide timeout policy change.
	s.Handle(http.MethodGet, "/reports/slow", func(r *http.Request) (any, error) {
		select {
		case <-time.After(2 * time.Second):
			return map[string]string{"status": "complete"}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}, servekit.WithEndpointTimeout(750*time.Millisecond))

	// High-frequency internal routes can opt out of noisy observability.
	s.Handle(http.MethodGet, "/internal/pulse", func(r *http.Request) (any, error) {
		return map[string]string{"status": "ok"}, nil
	}, servekit.WithSkipAccessLog(), servekit.WithSkipTelemetry())

	log.Println("endpoint-controls example listening on :8091")
	log.Println("what this example demonstrates:")
	log.Println(`  - WithMiddleware(...) for headers or policy shared across every route`)
	log.Println(`  - WithAuthCheck(...) for simple HTTP 401 route protection`)
	log.Println(`  - WithAuthGate(...) for richer route-specific auth failures`)
	log.Println(`  - WithEndpointMiddleware(...) for one-route-only headers or policy`)
	log.Println(`  - WithBodyLimit(...) and WithEndpointTimeout(...) without changing the whole server`)
	log.Println(`  - WithSkipAccessLog() and WithSkipTelemetry() for noisy internal routes`)
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8091/admin/ping`)
	log.Println(`  curl -i -H 'X-Admin-Token: anything' http://127.0.0.1:8091/admin/ping`)
	log.Println(`  curl -i -X POST -d '{"publish":true}' http://127.0.0.1:8091/admin/publish`)
	log.Println(`  curl -i -X POST -H 'X-Admin-Token: local-dev' -d '{"publish":true}' http://127.0.0.1:8091/admin/publish`)
	log.Println(`  curl -i -X POST -d 'small avatar' http://127.0.0.1:8091/uploads/avatar`)
	log.Println(`  curl -i http://127.0.0.1:8091/reports/slow`)
	log.Println(`  curl -i http://127.0.0.1:8091/internal/pulse`)
	log.Println(`  # larger upload bodies will return HTTP 413 on /uploads/avatar`)

	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

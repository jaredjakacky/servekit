package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The readiness example focuses on startup sequencing, readiness checks,
// custom health reporting, and graceful shutdown behavior. Read it when a
// service should not report ready immediately on process start.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var cacheWarmed atomic.Bool

	// Simulate startup work that must finish before the service should receive
	// normal traffic.
	go func() {
		log.Println("warming cache for 5 seconds")
		time.Sleep(5 * time.Second)
		cacheWarmed.Store(true)
		log.Println("cache warmup complete")
	}()

	s := servekit.New(
		servekit.WithAddr(":8081"),
		servekit.WithShutdownDrainDelay(5*time.Second),
		servekit.WithShutdownTimeout(20*time.Second),
		servekit.WithReadinessChecks(func(ctx context.Context) error {
			if !cacheWarmed.Load() {
				return errors.New("cache not warmed")
			}
			return nil
		}),
		// /healthz is optional and user-owned. This example shows how a service can
		// publish richer health detail without replacing the built-in probe story.
		servekit.WithHealthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			status := http.StatusOK
			body := `{"status":"ok","cache":"warm"}`
			if !cacheWarmed.Load() {
				status = http.StatusServiceUnavailable
				body = `{"status":"degraded","cache":"warming"}`
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})),
	)

	// This route shows request-scoped cancellation working with endpoint timeouts
	// and graceful shutdown. It will exit early if the request context is canceled.
	s.Handle(http.MethodGet, "/slow", func(r *http.Request) (any, error) {
		select {
		case <-time.After(3 * time.Second):
			return map[string]string{"status": "finished"}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}, servekit.WithEndpointTimeout(10*time.Second))

	log.Println("readiness example listening on :8081")
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8081/readyz`)
	log.Println(`  curl -i http://127.0.0.1:8081/healthz`)
	log.Println(`  curl -i http://127.0.0.1:8081/slow`)
	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

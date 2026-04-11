package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The telemetry example is the focused OpenTelemetry story for Servekit.
// Read it when you want to see the default tracing and metrics behavior
// directly instead of only hearing that the hooks exist.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// This installs process-wide tracer and meter providers. Servekit's default
	// OTel middleware uses those global providers automatically, so the example
	// does not need any Servekit-specific telemetry options.
	shutdownTelemetry, err := installTelemetry()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdownTelemetry(context.Background()); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}()

	s := servekit.New(
		servekit.WithAddr(":8090"),
	)

	// This route shows the default request span and context helpers on the
	// normal Handle path.
	s.Handle(http.MethodGet, "/widgets/{id}", func(r *http.Request) (any, error) {
		return map[string]any{
			"id":         r.PathValue("id"),
			"request_id": servekit.RequestIDFromContext(r.Context()),
			"trace_id":   servekit.TraceIDFromContext(r.Context()),
			"span_id":    servekit.SpanIDFromContext(r.Context()),
		}, nil
	})

	// This route creates a non-200 response so the stdout metric export shows
	// Servekit recording request outcome as well as volume.
	s.Handle(http.MethodGet, "/fail", func(r *http.Request) (any, error) {
		return nil, servekit.Error(http.StatusBadGateway, "upstream unavailable", nil)
	})

	// Keep one request open long enough for the periodic metric reader to export
	// visible in-flight request and active-connection values on the Run path.
	s.Handle(http.MethodGet, "/slow", func(r *http.Request) (any, error) {
		select {
		case <-time.After(5 * time.Second):
			return map[string]string{"status": "finished"}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})

	log.Println("telemetry example listening on :8090")
	log.Println("what this example demonstrates:")
	log.Println(`  - Servekit uses the global tracer and meter providers by default`)
	log.Println(`  - request spans print immediately to stdout`)
	log.Println(`  - request metrics export every 2 seconds`)
	log.Println(`  - because this example uses Run(...), connection metrics export too`)
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8090/widgets/123`)
	log.Println(`  curl -i -H "traceparent: 00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01" http://127.0.0.1:8090/widgets/123`)
	log.Println(`  curl -i http://127.0.0.1:8090/fail`)
	log.Println(`  curl -i http://127.0.0.1:8090/slow`)
	log.Println("watch stdout for:")
	log.Println(`  - spans named like "GET /widgets/{id}"`)
	log.Println(`  - request metrics such as http.server.request.count`)
	log.Println(`  - connection metrics such as http.server.connection.active while /slow is in flight`)

	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

package main

import (
	"context"
	"log"
	"net/http"

	servekit "github.com/jaredjakacky/servekit"
)

// The basic example is intentionally boring at the application layer.
// The point is to show how little code the service itself needs once
// Servekit owns the operational baseline.
func main() {
	// This installs a process-wide OpenTelemetry exporter.
	//
	// Servekit enables OTel middleware by default and uses the global provider,
	// so once this is set, Servekit will automatically emit request spans
	// without any Servekit-specific telemetry configuration.
	shutdownTelemetry, err := installTelemetry()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdownTelemetry(context.Background()); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}()

	// One call to New and Servekit gives you probes, IDs, logging, panic
	// recovery, OTel middleware, and other useful production defaults.
	s := servekit.New()

	s.Handle(http.MethodGet, "/hello/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		return map[string]any{
			"message": "hello " + name,
		}, nil
	})

	log.Println("basic example listening on :8080")
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8080/hello/jared`)
	log.Println(`  curl -i -H "traceparent: 00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01" http://127.0.0.1:8080/hello/jared`)
	log.Println(`  curl -i http://127.0.0.1:8080/livez`)
	log.Println(`  curl -i http://127.0.0.1:8080/readyz`)
	log.Println(`  curl -i http://127.0.0.1:8080/version`)
	log.Println("Servekit production defaults in this example:")
	log.Println(`  - JSON success/error encoding for Handle(...)`)
	log.Println(`  - built-in GET /livez, GET /readyz, and GET /version`)
	log.Println(`  - automatic X-Request-ID and X-Correlation-ID headers`)
	log.Println(`  - built-in access logs and panic recovery`)
	log.Println(`  - built-in OpenTelemetry request middleware`)
	log.Println("telemetry note:")
	log.Println(`  - this example configures a global stdout exporter`)
	log.Println(`  - Servekit automatically uses that global OTel provider`)
	log.Println(`  - request spans will be printed to stdout when you hit the routes`)
	if err := s.Run(context.Background()); err != nil {
		log.Printf("serve: %v", err)
	}
}

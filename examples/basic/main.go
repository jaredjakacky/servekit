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
	log.Println(`  - the host application owns OTel providers and exporters`)
	log.Println(`  - see examples/telemetry for visible stdout spans and metrics`)
	if err := s.Run(context.Background()); err != nil {
		log.Printf("serve: %v", err)
	}
}

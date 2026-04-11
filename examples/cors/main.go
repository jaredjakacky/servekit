package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	servekit "github.com/jaredjakacky/servekit"
)

// The CORS example shows how to add browser-facing CORS policy without
// changing the rest of the Servekit service model. Read it when a service must
// answer preflight and actual cross-origin requests.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := servekit.New(
		servekit.WithAddr(":8087"),
		servekit.WithCORSConfig(servekit.CORSConfig{
			AllowedOrigins:   []string{"http://localhost:3000"},
			AllowedMethods:   []string{http.MethodGet, http.MethodPost},
			AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Request-ID"},
			ExposedHeaders:   []string{"X-Request-ID"},
			AllowCredentials: true,
			MaxAge:           600,
		}),
	)

	// CORS is middleware-level behavior. The application routes stay normal.
	s.Handle(http.MethodGet, "/profile", func(r *http.Request) (any, error) {
		return map[string]any{
			"user": "jared",
			"role": "admin",
		}, nil
	})

	s.Handle(http.MethodPost, "/messages", func(r *http.Request) (any, error) {
		return map[string]string{"status": "accepted"}, nil
	})

	log.Println("cors example listening on :8087")
	log.Println("try preflight:")
	log.Println(`  curl -i -X OPTIONS -H 'Origin: http://localhost:3000' -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: Authorization, Content-Type' http://127.0.0.1:8087/messages`)
	log.Println("try actual request:")
	log.Println(`  curl -i -H 'Origin: http://localhost:3000' http://127.0.0.1:8087/profile`)
	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

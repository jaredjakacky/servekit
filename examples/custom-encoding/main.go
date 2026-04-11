package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	servekit "github.com/jaredjakacky/servekit"
)

// The custom-encoding example shows how to keep the Servekit route model while
// enforcing a different response contract globally and per endpoint. Read it
// when the service already has a house response schema to preserve.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := servekit.New(
		servekit.WithAddr(":8086"),
		servekit.WithResponseEncoder(func(w http.ResponseWriter, r *http.Request, payload any) error {
			if payload == nil {
				w.WriteHeader(http.StatusNoContent)
				return nil
			}

			var body bytes.Buffer
			if err := json.NewEncoder(&body).Encode(map[string]any{
				"ok":         true,
				"result":     payload,
				"request_id": servekit.RequestIDFromContext(r.Context()),
			}); err != nil {
				return err
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write(body.Bytes())
			return err
		}),
		servekit.WithErrorEncoder(func(w http.ResponseWriter, r *http.Request, err error) error {
			status := http.StatusInternalServerError
			message := "internal server error"

			var httpErr servekit.HTTPError
			switch {
			case errors.As(err, &httpErr):
				if httpErr.StatusCode > 0 {
					status = httpErr.StatusCode
				}
				if httpErr.Message != "" {
					message = httpErr.Message
				}
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
				message = "request timed out"
			case errors.Is(err, context.Canceled):
				status = http.StatusGatewayTimeout
				message = "request canceled"
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			return json.NewEncoder(w).Encode(map[string]any{
				"ok": false,
				"error": map[string]any{
					"status":  status,
					"message": message,
				},
				"request_id": servekit.RequestIDFromContext(r.Context()),
			})
		}),
	)

	// This route uses the server-wide custom success encoder.
	s.Handle(http.MethodGet, "/widgets", func(r *http.Request) (any, error) {
		return []map[string]any{
			{"id": "123", "name": "hammer"},
			{"id": "456", "name": "wrench"},
		}, nil
	})

	// Returning servekit.Error lets the handler control status and message while
	// still delegating response writing to the configured error encoder.
	s.Handle(http.MethodGet, "/widgets/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		if id == "missing" {
			return nil, servekit.Error(http.StatusNotFound, "widget not found", nil)
		}
		return map[string]any{"id": id, "name": "example-widget"}, nil
	})

	// One route can still opt into its own success encoder without changing the
	// rest of the server's response contract.
	s.Handle(http.MethodGet, "/plaintext", func(r *http.Request) (any, error) {
		return "hello from a per-route encoder", nil
	}, servekit.WithEndpointResponseEncoder(func(w http.ResponseWriter, _ *http.Request, payload any) error {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(payload.(string) + "\n"))
		return err
	}))

	log.Println("custom-encoding example listening on :8086")
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8086/widgets`)
	log.Println(`  curl -i http://127.0.0.1:8086/widgets/123`)
	log.Println(`  curl -i http://127.0.0.1:8086/widgets/missing`)
	log.Println(`  curl -i http://127.0.0.1:8086/plaintext`)
	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

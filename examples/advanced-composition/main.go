package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	stdlog "log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// The advanced-composition example is an intentionally beefy reference
// configuration. It is not the recommended starting point. Its job is to show
// how several advanced hooks can coexist in one real Servekit service without
// abandoning the package's normal model.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var warmed atomic.Bool

	appLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})).With(
		"service", "servekit-example",
		"example", "advanced-composition",
	)
	httpServerLog := slog.NewLogLogger(appLogger.Handler(), slog.LevelWarn)

	s := servekit.New(
		servekit.WithAddr(":8089"),
		servekit.WithLogger(appLogger),
		servekit.WithHTTPServerErrorLog(httpServerLog),
		servekit.WithBuildInfo("advanced-demo", "abc1234", "2026-04-06T00:00:00Z"),
		servekit.WithShutdownDrainDelay(2*time.Second),
		servekit.WithShutdownTimeout(10*time.Second),
		servekit.WithRequestBodyLimit(1<<20),
		servekit.WithCORSConfig(servekit.CORSConfig{
			AllowedOrigins: []string{"http://localhost:3000"},
			AllowedMethods: []string{http.MethodGet, http.MethodPost},
			AllowedHeaders: []string{"Content-Type", "X-Admin-Token"},
			ExposedHeaders: []string{"X-Request-ID"},
			MaxAge:         300,
		}),
		servekit.WithReadinessChecks(func(ctx context.Context) error {
			if !warmed.Load() {
				return errors.New("startup warmup incomplete")
			}
			return nil
		}),
		servekit.WithHealthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			status := http.StatusOK
			body := map[string]any{
				"status": "ok",
				"cache":  "warm",
			}
			if !warmed.Load() {
				status = http.StatusServiceUnavailable
				body["status"] = "degraded"
				body["cache"] = "warming"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		})),
		servekit.WithResponseEncoder(houseSuccessEncoder),
		servekit.WithErrorEncoder(houseErrorEncoder),
		servekit.WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Service", "advanced-composition")
				next.ServeHTTP(w, r)
			})
		}),
		servekit.WithTracerProvider(tracenoop.NewTracerProvider()),
		servekit.WithMeterProvider(metricnoop.NewMeterProvider()),
		servekit.WithOTelAttributes(func(r *http.Request) []attribute.KeyValue {
			return []attribute.KeyValue{
				attribute.String("servekit.example", "advanced-composition"),
				attribute.String("servekit.request_id", servekit.RequestIDFromContext(r.Context())),
			}
		}),
		servekit.WithSpanNameFormatter(func(r *http.Request, route string) string {
			if route == "" {
				route = r.URL.Path
			}
			return r.Method + " " + route
		}),
		servekit.WithRouteLabeler(func(r *http.Request) string {
			if r.Pattern != "" {
				return "advanced:" + r.Pattern
			}
			return "advanced:" + r.URL.Path
		}),
	)

	// This example owns readiness explicitly so startup work can finish before
	// /readyz begins reporting success.
	s.SetReady(false)
	go func() {
		appLogger.Info("warming dependencies", "delay", "3s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		warmed.Store(true)
		s.SetReady(true)
		appLogger.Info("server marked ready")
	}()

	// Normal structured route using the global response and error encoders.
	s.Handle(http.MethodGet, "/widgets/{id}", func(r *http.Request) (any, error) {
		id := r.PathValue("id")
		if id == "missing" {
			return nil, servekit.Error(http.StatusNotFound, "widget not found", nil)
		}
		return map[string]any{
			"id":         id,
			"owner":      "advanced-example",
			"request_id": servekit.RequestIDFromContext(r.Context()),
			"trace_id":   servekit.TraceIDFromContext(r.Context()),
		}, nil
	})

	// This route combines several endpoint-level overrides without changing the
	// rest of the server: auth gate, timeout, body limit, and local middleware.
	s.Handle(http.MethodPost, "/admin/reindex", func(r *http.Request) (any, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}

		select {
		case <-time.After(200 * time.Millisecond):
			return map[string]any{
				"status": "accepted",
				"bytes":  len(body),
				"actor":  "ops",
			}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
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
		servekit.WithEndpointTimeout(3*time.Second),
		servekit.WithBodyLimit(128<<10),
	)

	// A single route can still break away from the global success contract.
	s.Handle(http.MethodGet, "/reports/plain", func(r *http.Request) (any, error) {
		return "advanced composition report", nil
	}, servekit.WithEndpointResponseEncoder(func(w http.ResponseWriter, _ *http.Request, payload any) error {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, err := io.WriteString(w, payload.(string)+"\n")
		return err
	}))

	// High-frequency internal endpoints often want a lighter observability
	// footprint without affecting the rest of the service.
	s.Handle(http.MethodGet, "/internal/pulse", func(r *http.Request) (any, error) {
		return map[string]string{"status": "ok"}, nil
	}, servekit.WithSkipAccessLog(), servekit.WithSkipTelemetry())

	// Raw handlers still fit when direct response control is the better shape.
	s.HandleHTTP(http.MethodGet, "/debug/request", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"method":         r.Method,
			"route":          r.Pattern,
			"request_id":     servekit.RequestIDFromContext(r.Context()),
			"correlation_id": servekit.CorrelationIDFromContext(r.Context()),
			"trace_id":       servekit.TraceIDFromContext(r.Context()),
			"span_id":        servekit.SpanIDFromContext(r.Context()),
		})
	}))

	stdlog.Println("advanced composition example listening on :8089")
	stdlog.Println("try:")
	stdlog.Println(`  curl -i http://127.0.0.1:8089/readyz`)
	stdlog.Println(`  curl -i http://127.0.0.1:8089/healthz`)
	stdlog.Println(`  curl -i http://127.0.0.1:8089/widgets/123`)
	stdlog.Println(`  curl -i http://127.0.0.1:8089/widgets/missing`)
	stdlog.Println(`  curl -i http://127.0.0.1:8089/reports/plain`)
	stdlog.Println(`  curl -i http://127.0.0.1:8089/internal/pulse`)
	stdlog.Println(`  curl -i http://127.0.0.1:8089/debug/request`)
	stdlog.Println(`  curl -i -X POST -d '{"scope":"all"}' http://127.0.0.1:8089/admin/reindex`)
	stdlog.Println(`  curl -i -X POST -H 'X-Admin-Token: local-dev' -d '{"scope":"all"}' http://127.0.0.1:8089/admin/reindex`)
	if err := s.Run(ctx); err != nil {
		stdlog.Printf("serve: %v", err)
	}
}

func houseSuccessEncoder(w http.ResponseWriter, r *http.Request, payload any) error {
	if payload == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(map[string]any{
		"ok":         true,
		"data":       payload,
		"request_id": servekit.RequestIDFromContext(r.Context()),
		"trace_id":   servekit.TraceIDFromContext(r.Context()),
	})
}

func houseErrorEncoder(w http.ResponseWriter, r *http.Request, err error) error {
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
		"trace_id":   servekit.TraceIDFromContext(r.Context()),
	})
}

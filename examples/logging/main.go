package main

import (
	"context"
	stdlog "log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	servekit "github.com/jaredjakacky/servekit"
)

// The logging example shows how Servekit fits into a custom slog setup while
// still providing its built-in access logging and panic recovery behavior.
// Read it when the service already has a house logging policy.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// This slog logger is the main logger for Servekit middleware and for
	// application code that wants to log request-level events.
	appLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})).With(
		"service", "servekit-example",
		"example", "logging",
	)

	// http.Server.ErrorLog uses the older stdlib log.Logger type. This explicit
	// bridge keeps the same output handler but tags server-internal records at a
	// chosen severity.
	httpServerLog := slog.NewLogLogger(appLogger.Handler(), slog.LevelError)

	s := servekit.New(
		servekit.WithAddr(":8082"),
		servekit.WithLogger(appLogger),
		servekit.WithHTTPServerErrorLog(httpServerLog),
	)

	s.Handle(http.MethodGet, "/hello", func(r *http.Request) (any, error) {
		appLogger.Info("handling request", "path", r.URL.Path, "request_id", servekit.RequestIDFromContext(r.Context()))
		return map[string]string{"message": "check the logs"}, nil
	})

	s.Handle(http.MethodGet, "/boom", func(r *http.Request) (any, error) {
		return nil, servekit.Error(http.StatusBadGateway, "upstream unavailable", nil)
	})

	// This route forces a server-side panic to show Recovery logging behavior.
	s.HandleHTTP(http.MethodGet, "/panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("forced example panic")
	}))

	stdlog.Println("logging example listening on :8082")
	stdlog.Println("try:")
	stdlog.Println(`  curl -i http://127.0.0.1:8082/hello`)
	stdlog.Println(`  curl -i http://127.0.0.1:8082/boom`)
	stdlog.Println(`  curl -i http://127.0.0.1:8082/panic`)
	if err := s.Run(ctx); err != nil {
		stdlog.Printf("serve: %v", err)
	}
}

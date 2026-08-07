package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The external-server example shows the advanced path where Servekit owns
// routes, middleware, probes, and response behavior while another http.Server
// owns transport policy and lifecycle. Read it when the application already
// has server infrastructure and only wants Servekit's wrapped handler stack.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := ":8088"
	shutdownTimeout := 10 * time.Second

	mux := http.NewServeMux()
	mux.HandleFunc("GET /legacy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello from a pre-existing mux route\n"))
	})

	s := servekit.New(
		servekit.WithMux(mux),
	)

	s.Handle(http.MethodGet, "/hello", func(r *http.Request) (any, error) {
		return map[string]string{"message": "hello from servekit"}, nil
	})

	// Handler() lets another part of the application own the outer server.
	// In this mode, transport settings such as the listen address and
	// read/write/idle/header timeout policy belong on the outer http.Server,
	// not on Servekit options that only apply to Run().
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Servekit still serves /readyz, but readiness is application-owned because
	// this example does not use Run().
	go func() {
		log.Println("warming dependencies for 2 seconds before advertising readiness")
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		s.SetReady(true)
		log.Println("server marked ready")
	}()

	// The application owns graceful shutdown and forced-close fallback when it
	// owns the outer http.Server instead of using Servekit's Run method.
	shutdownErrCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		s.SetReady(false)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()

		var closeErr error
		if shutdownErr != nil {
			closeErr = server.Close()
		}
		shutdownErrCh <- errors.Join(
			wrapLifecycleError("shutdown", shutdownErr),
			wrapLifecycleError("close", closeErr),
		)
	}()

	log.Printf("external-server example listening on %s", addr)
	log.Println("try:")
	log.Printf("  curl -i http://127.0.0.1%s/readyz", addr)
	log.Printf("  curl -i http://127.0.0.1%s/legacy", addr)
	log.Printf("  curl -i http://127.0.0.1%s/hello", addr)
	log.Printf("  curl -i http://127.0.0.1%s/version", addr)
	serveErr := server.ListenAndServe()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	} else if serveErr != nil {
		serveErr = fmt.Errorf("serve: %w", serveErr)
		stop()
	}

	if err := errors.Join(serveErr, <-shutdownErrCh); err != nil {
		log.Fatal(err)
	}
}

func wrapLifecycleError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

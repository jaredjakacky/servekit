package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The response-capture example is more implementation-focused. Read it when
// you want to see why raw `HandleHTTP(...)` routes remain technically credible:
// implicit status capture, byte observation, `http.Flusher`, and a concrete
// `http.Hijacker` route.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	s := servekit.New(
		servekit.WithAddr(":8085"),
		servekit.WithLogger(logger),
	)

	// This route never calls WriteHeader explicitly. net/http will still commit
	// an implicit 200 on the first body write, and Servekit's response capture
	// wrapper mirrors that behavior so logs and telemetry can report the final
	// status correctly.
	s.HandleHTTP(http.MethodGet, "/implicit-ok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("implicit success\n"))
	}))

	// This route commits a non-200 status before writing the body. The wrapper
	// observes that committed status and the response body bytes accepted by the
	// normal response-writing path.
	s.HandleHTTP(http.MethodGet, "/teapot", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout\n"))
	}))

	// Streaming only works if the wrapped ResponseWriter still exposes
	// http.Flusher. Servekit preserves that capability so HandleHTTP routes can
	// stream through middleware while access logs still see the final status and
	// body byte count.
	s.HandleHTTP(http.MethodGet, "/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		for i := 1; i <= 3; i++ {
			_, _ = fmt.Fprintf(w, "data: tick %d\n\n", i)
			flusher.Flush()
			time.Sleep(1 * time.Second)
		}
	}))

	// Hijack is the point where normal ResponseWriter semantics end and the
	// handler takes over the connection directly. This is an HTTP/1.x-style raw
	// connection escape hatch, not an HTTP/2 capability.
	//
	// This route still exercises response capture in one useful way: it proves
	// Servekit did not hide http.Hijacker from a raw HandleHTTP handler.
	// But once Hijack succeeds, later writes are no longer going through
	// http.ResponseWriter at all, so Servekit cannot observe post-hijack body
	// bytes. Access logs will therefore show the default status and zero body
	// bytes for the hijacked portion of the exchange.
	s.HandleHTTP(http.MethodGet, "/hijack", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}

		conn, connRW, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()

		// This Flush is on the bufio.ReadWriter returned by Hijack, not on
		// http.Flusher. Once the connection is hijacked, the handler owns the raw
		// buffered connection and can flush it directly.
		_, _ = connRW.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = connRW.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		_, _ = connRW.WriteString("Connection: close\r\n")
		_, _ = connRW.WriteString("\r\n")
		_, _ = connRW.WriteString("hello from a hijacked connection\n")
		_ = connRW.Flush()
	}))

	log.Println("response capture example listening on :8085")
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8085/implicit-ok`)
	log.Println(`  curl -i http://127.0.0.1:8085/teapot`)
	log.Println(`  curl -N http://127.0.0.1:8085/events`)
	log.Println(`  curl -i --http1.1 http://127.0.0.1:8085/hijack`)
	log.Println("watch the access logs for status and bytes on each request")
	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

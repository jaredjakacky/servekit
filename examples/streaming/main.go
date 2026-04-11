package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The streaming example shows why `HandleHTTP(...)` exists: some endpoints
// need to write incrementally to the client rather than return one payload.
// Read it when you want the concrete `http.Flusher` example in this repo.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := servekit.New(
		servekit.WithAddr(":8083"),
		// Real SSE routes are often intentionally long-lived, so the default
		// server write timeout is frequently too short.
		servekit.WithWriteTimeout(0),
		// For partial streams, abort-style semantics are usually more correct
		// than trying to synthesize a fallback JSON 500 mid-stream.
		servekit.WithPanicPropagation(true),
	)

	// Streaming is a strong reason to choose HandleHTTP. The route writes chunks
	// over time and flushes them immediately, which does not fit the one-payload
	// return model used by Handle.
	s.HandleHTTP(http.MethodGet, "/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		for i := 1; i <= 5; i++ {
			_, _ = fmt.Fprintf(w, "data: tick %d\n\n", i)
			flusher.Flush()
			time.Sleep(1 * time.Second)
		}
	}))

	log.Println("streaming example listening on :8083")
	log.Println("try:")
	log.Println(`  curl -N http://127.0.0.1:8083/events`)
	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

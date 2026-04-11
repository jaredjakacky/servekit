package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/signal"
	"syscall"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

// The reverse-proxy example shows how to mount a raw proxy handler through
// Servekit. Read it when the route should keep HTTP-level request/response
// control while the service keeps the surrounding middleware, probes, and
// lifecycle behavior.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start a tiny upstream service so this example is runnable on its own.
	upstream := &http.Server{
		Addr: ":9090",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"source": "upstream",
				"path":   r.URL.Path,
				"header": r.Header.Get("X-Forwarded-By"),
			})
		}),
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		log.Println("upstream service listening on :9090")
		if err := upstream.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("upstream serve: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = upstream.Shutdown(shutdownCtx)
	}()

	targetURL, err := url.Parse("http://127.0.0.1:9090")
	if err != nil {
		log.Fatal(err)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()

			// Request changes happen before the upstream call is made.
			pr.Out.Header.Set("X-Forwarded-By", "servekit")
		},
		ModifyResponse: func(res *http.Response) error {
			// Response changes happen after the upstream responds but before the
			// original client sees the result.
			res.Header.Set("X-Proxied-By", "servekit")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream unavailable"})
		},
		Transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	s := servekit.New(
		servekit.WithAddr(":8084"),
	)

	// Reverse proxies already speak in raw HTTP terms, so HandleHTTP is the
	// right fit: Servekit mounts the handler, but the proxy owns request and
	// response flow.
	//
	// This demo keeps Servekit's default write timeout because the proxied
	// response is short-lived. For long-lived proxy traffic, choose
	// WithWriteTimeout(...) together with the proxy Transport timeouts, and
	// consider WithPanicPropagation(true) when connection-abort semantics are
	// more correct than a fallback JSON 500.
	s.HandleHTTP(http.MethodGet, "/upstream", proxy)

	log.Println("reverse proxy example listening on :8084")
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8084/upstream`)
	if err := s.Run(ctx); err != nil {
		log.Printf("serve: %v", err)
	}
}

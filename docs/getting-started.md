# Getting Started

Use this guide for the shortest path from `go get` to a running service. It stays on the normal Servekit path:

1. install the package
2. create a server
3. register one route
4. run it
5. hit the built-in operational endpoints

## Install

```bash
go get github.com/jaredjakacky/servekit
```

Servekit's minimum supported Go version is declared in [`go.mod`](../go.mod). The Go versions currently verified in CI are listed in [`.github/workflows/ci.yaml`](../.github/workflows/ci.yaml).

## Build a first service

This example keeps the application route simple so the Servekit defaults stay easy to see.

```go
package main

import (
	"context"
	"log"
	"net/http"

	servekit "github.com/jaredjakacky/servekit"
)

func main() {
	s := servekit.New()

	s.Handle(http.MethodGet, "/hello/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		return map[string]string{"message": "hello " + name}, nil
	})

	if err := s.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

The handler returns one payload or one error. Servekit handles the rest of the normal API path around it.

This guide intentionally keeps the first service as small as possible. The runnable [`examples/basic`](../examples/basic) example adds a global stdout OpenTelemetry exporter so Servekit's default tracing is visible immediately, but the Servekit flow is the same.

## Run it

From your own module, run your main package, for example:

```bash
go run ./cmd/your-service
```

If you are just exploring locally in this repository, run the existing example instead:

```bash
go run ./examples/basic
```

## Try the routes

Application route:

```bash
curl http://127.0.0.1:8080/hello/jared
```

Built-in operational endpoints:

```bash
curl -i http://127.0.0.1:8080/livez
curl -i http://127.0.0.1:8080/readyz
curl -i http://127.0.0.1:8080/version
```

Expected behavior:

- `/hello/{name}` returns a JSON response shaped like `{"data": ...}`
- `/livez` returns `200 OK`
- `/readyz` returns `200 OK` once the server is serving traffic
- `/version` returns build and Go runtime metadata
- handled requests also emit a structured access log entry by default
- if the application installs a global OpenTelemetry provider, Servekit emits request spans automatically

## What you get from `New()`

A fresh `servekit.New()` server starts with:

- built-in middleware for panic recovery, OpenTelemetry, request IDs, correlation IDs, and access logs
- built-in `GET /livez`, `GET /readyz`, and `GET /version`
- JSON response and error encoding for `Handle`
- conservative `http.Server` timeouts
- a default request body limit

That gives a new service a real HTTP baseline without leaving the underlying `net/http` model. You still work with `http.Request`, `http.Handler`, and `http.ServeMux`.

Servekit's default OTel middleware uses the global tracer provider and propagator unless you override them. That means your application can install its normal process-wide OTel setup and Servekit will pick it up automatically. The repository's [`examples/basic`](../examples/basic) example does exactly that with a stdout exporter so the default tracing behavior is easy to see.

## How routing works

Servekit uses method-plus-path patterns on the underlying `http.ServeMux`. In practice that means calls like:

```go
s.Handle(http.MethodGet, "/hello/{name}", h)
s.HandleHTTP(http.MethodPost, "/upload", raw)
```

Because the underlying router is still the Go standard library, path variables and matching behavior come from `http.ServeMux`, not from a Servekit-specific routing DSL.

## Next steps

- Read [Usage Guide](usage.md) for the full server model and per-endpoint controls.
- Read [Advanced Guide](advanced.md) when the service needs custom encoders, an existing mux, or an externally owned `http.Server`.
- Read [Lifecycle and Probes](lifecycle.md) if your service has warmup work or explicit readiness logic.
- Read [Observability and Middleware](observability.md) if you want to customize logs, tracing, or CORS.
- Read [API Map](api.md) for the complete exported surface.

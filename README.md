# Servekit

[![Release](https://img.shields.io/github/v/release/jaredjakacky/servekit?sort=semver)](https://github.com/jaredjakacky/servekit/releases)
[![CI](https://github.com/jaredjakacky/servekit/actions/workflows/ci.yaml/badge.svg)](https://github.com/jaredjakacky/servekit/actions/workflows/ci.yaml)
[![Go Support](https://img.shields.io/badge/go%20support-1.25.x%20%7C%201.26.x-00ADD8)](https://github.com/jaredjakacky/servekit/actions/workflows/ci.yaml)
[![License](https://img.shields.io/github/license/jaredjakacky/servekit)](https://github.com/jaredjakacky/servekit/blob/main/LICENSE)

## Overview

Servekit is a small Go package for bootstrapping HTTP services on top of `net/http`. It gives a service a real operational baseline from the first constructor call: probes, JSON response handling, request and correlation IDs, access logs, panic recovery, graceful shutdown, opt-in CORS, and built-in OpenTelemetry tracing and metrics.

It is especially useful for APIs and microservices that want consistent HTTP bootstrap without adopting a full web framework.

Servekit is not a framework. You still work with `http.Request`, `http.Handler`, and `http.ServeMux`. The point is to stop rebuilding the same service bootstrap around them.

It also does not lock you into only the built-in stack. Services can add their own global middleware with `WithMiddleware(...)` and route-local middleware with `WithEndpointMiddleware(...)` while staying on the normal Servekit path.

## Why Servekit exists

Many Go services spend a meaningful amount of code on the same HTTP setup work: configuring `http.Server`, wiring middleware, handling panics, exposing probes, publishing build information, and shutting down cleanly.

That work matters, but it usually gets rewritten service by service and drifts a little each time. Servekit pulls it into a small, `net/http`-first package so new services can start from one coherent baseline and spend more code on domain behavior.

Its goal is narrow: own the reusable HTTP bootstrap layer, not the whole application.

## What Servekit is not

Servekit is not a web framework. It does not replace `net/http`, add its own router DSL, or impose a different application model.

It also does not try to own dependency injection, background work, config loading, or service discovery. Those concerns stay with the application.

It is not a full observability platform either. Servekit gives the HTTP layer a strong default baseline, but the application still owns its telemetry backend, dashboards, alerting, and broader operational policy.

## Good fit / not a fit

Servekit is a good fit when:

- you want to stay on `net/http` but stop rebuilding the same service bootstrap around it
- you want built-in probes, readiness, request IDs, access logging, panic recovery, OpenTelemetry, and graceful shutdown from the start
- you want strong defaults without giving up application-owned middleware
- you want one package that can cover both ordinary request/response routes and raw `http.Handler` endpoints such as streaming, proxying, and upgrades

Servekit is probably not a fit when:

- you want a batteries-included web framework or a non-stdlib routing model
- your service already has a settled bootstrap stack and Servekit would just duplicate it
- you mainly want a full framework abstraction rather than a `net/http`-first bootstrap layer

## Installation

```bash
go get github.com/jaredjakacky/servekit
```

```go
import servekit "github.com/jaredjakacky/servekit"
```

## Quick Start

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

	s.Handle(http.MethodGet, "/coffee", func(r *http.Request) (any, error) {
		return map[string]string{
			"drink":  "coffee",
			"status": "ready",
		}, nil
	})

	if err := s.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

`Run()` starts the server on the configured address, which defaults to `:8080`, marks it ready once it begins serving, and handles shutdown on cancellation or `SIGINT` / `SIGTERM`.

That one server already gives you:

- JSON success and error encoding for `Handle`
- built-in `GET /livez`, `GET /readyz`, and `GET /version`
- request IDs and correlation IDs
- access logging and panic recovery
- OpenTelemetry request tracing and request metrics
- `Run(...)`-path connection metrics
- readiness transitions and graceful shutdown on `SIGINT` and `SIGTERM`
- conservative timeout and request body defaults
- built-in use of global OpenTelemetry providers and propagators unless you override them

When the service needs application-specific policy on top of that baseline, add global middleware with `WithMiddleware(...)` or route-local behavior with `WithEndpointMiddleware(...)` rather than replacing the whole setup.

By default, panic recovery logs the panic and stack trace, returns a best-effort JSON `500` when the response is still uncommitted, and leaves committed responses alone.

In practice, you get a real HTTP baseline without hand-building the `http.Server` lifecycle, middleware stack, probes, IDs, telemetry, and default request/response behavior yourself.

## The Core Model

Servekit is deliberately built around one normal path and one escape hatch.

### Normal path

Use `Handle` for the endpoints that naturally want to:

1. inspect the request
2. do application work
3. return one payload or one error

```go
s.Handle(http.MethodGet, "/users/me", func(r *http.Request) (any, error) {
	return map[string]string{
		"id":   "123",
		"name": "jared",
	}, nil
})
```

### Escape hatch

Use `HandleHTTP` when the endpoint needs direct `net/http` control, such as
streaming, proxying, upgrades, or mounting an existing `http.Handler`:

```go
s.HandleHTTP(http.MethodGet, "/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	for i := 0; i < 3; i++ {
		_, _ = fmt.Fprintf(w, "data: tick %d\n\n", i)
		flusher.Flush()
		time.Sleep(500 * time.Millisecond)
	}
}))
```

Typical reasons are streaming, server-sent events, reverse proxying, protocol upgrades, or handlers that already exist as `http.Handler`. Servekit preserves runtime writer capabilities such as `http.Flusher`, `http.Hijacker`, and the `io.ReaderFrom` fast path when the underlying writer supports them, so the raw path stays credible.

Servekit also uses the underlying `http.ServeMux` routing model rather than inventing its own router DSL. That is intentional: the package is meant to standardize the service baseline around the standard library, not to replace the standard library routing story.

## Why This Works

Servekit rests on three choices:

1. It keeps the standard library visible.
2. It ships a coherent operational baseline instead of scattered one-off setup.
3. It lets the service become more specialized without forcing a rewrite onto a different abstraction model.

That is why the package can stay small without feeling toy-sized.

## Advanced Capabilities

Servekit has a short normal path, but it is not boxed into only the defaults. Advanced hooks include:

- global and route-level custom middleware
- custom success and error encoders, globally or per endpoint
- integration with an existing `http.ServeMux`
- mounting `Handler()` into your own `http.Server`
- explicit readiness control with `SetReady(...)`
- custom `slog` and `http.Server.ErrorLog` wiring
- CORS configuration
- OpenTelemetry provider, propagator, and labeling customization
- raw-response handling for streaming, proxying, and hijacking

The advanced path is documented in [docs/advanced.md](docs/advanced.md), including composition patterns for combining several hooks without losing the main Servekit model.

## Documentation

- [Getting Started](docs/getting-started.md): first service, first run, first curl
- [Usage Guide](docs/usage.md): the normal path, default behavior, and recommended adoption flow
- [Advanced Guide](docs/advanced.md): custom encoders, composition patterns, external server ownership, telemetry customization, and other advanced hooks
- [Lifecycle and Probes](docs/lifecycle.md): readiness, `/livez`, `/readyz`, `/healthz`, and shutdown
- [Observability and Middleware](docs/observability.md): IDs, access logs, panic recovery, OpenTelemetry, and CORS
- [API Map](docs/api.md): human-friendly map of the exported surface
- [Examples Guide](docs/examples.md): how the runnable examples build from the core path outward
- [Examples Directory](examples/README.md): quick index of the runnable example programs

## Examples

Runnable programs live in [`examples/`](examples), which includes a guided tour of the example set.

Recommended reading order:

1. [`examples/basic`](examples/basic)
2. [`examples/telemetry`](examples/telemetry)
3. [`examples/endpoint-controls`](examples/endpoint-controls)
4. [`examples/custom-encoding`](examples/custom-encoding)
5. [`examples/readiness`](examples/readiness)
6. [`examples/logging`](examples/logging)
7. [`examples/cors`](examples/cors)
8. [`examples/external-server`](examples/external-server)
9. [`examples/advanced-composition`](examples/advanced-composition)
10. [`examples/streaming`](examples/streaming)
11. [`examples/reverse-proxy`](examples/reverse-proxy)
12. [`examples/response-capture`](examples/response-capture)

## API Reference

The canonical symbol-level API documentation should live in Go doc comments so it stays accurate in editors and Go tooling. The repository-level companion is [docs/api.md](docs/api.md), which groups the exported surface into a human-oriented map.

## Maintenance

Servekit is a small open source library maintained on a best-effort basis.

The active development line lives on `main`, and that is the only line actively maintained unless explicitly noted otherwise. The minimum supported Go version is declared in [`go.mod`](go.mod), and the Go versions currently verified in CI are listed in [`.github/workflows/ci.yaml`](.github/workflows/ci.yaml).

Compatibility-impacting changes should be called out explicitly in release notes or release descriptions. Long-lived maintenance branches and backports are not planned unless explicitly noted.

## License

[MIT](LICENSE)

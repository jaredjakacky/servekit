# Examples Guide

Servekit's examples are a guided tour, not a random pile of demos. Start with the shortest useful service, then move outward into telemetry, route-level policy, lifecycle, customization, and raw HTTP escape hatches.

If you want the short directory index instead, use [Servekit Examples](../examples/README.md).

The [`examples/`](../examples/) directory includes a guided index, and the source comments in each example `main.go` explain the key ideas in place.

## Start here

### [`examples/basic`](../examples/basic)

This is the first example to read.

It shows the main Servekit promise in one place:

- `New`
- `Handle`
- `Run`
- built-in operational endpoints
- JSON encoding, IDs, access logs, and panic recovery by default
- a global stdout exporter installed only so the default OTel middleware is visible when you run it

If this example does not feel compelling, the rest of the package probably will not either.

### [`examples/telemetry`](../examples/telemetry)

Shows the stronger telemetry story directly:

- global tracer and meter providers
- request spans through Servekit's default OTel middleware
- request metrics through Servekit's default OTel middleware
- server connection metrics on the `Run(...)` path

Read this when you want proof that Servekit's telemetry story is real, not just configurable.

### [`examples/endpoint-controls`](../examples/endpoint-controls)

Shows the custom-middleware and route-level policy story:

- `WithMiddleware(...)`
- `WithAuthCheck(...)`
- `WithAuthGate(...)`
- `WithEndpointMiddleware(...)`
- `WithBodyLimit(...)`
- `WithEndpointTimeout(...)`
- route-local observability suppression

Read this when a service mostly wants the default server but needs a little application-owned global policy and a few routes with different local behavior.

## Build outward from the core path

### [`examples/custom-encoding`](../examples/custom-encoding)

Shows how a service can keep the Servekit model while enforcing a custom response contract:

- global success encoder override
- global error encoder override
- per-endpoint success encoder override
- `HTTPError`-based status control

### [`examples/readiness`](../examples/readiness)

Shows the lifecycle story:

- readiness checks
- explicit warmup behavior
- custom `/healthz`
- shutdown drain delay

### [`examples/logging`](../examples/logging)

Shows the logging story:

- custom `slog` logger
- explicit `http.Server.ErrorLog`
- request ID access from context
- panic recovery behavior

### [`examples/cors`](../examples/cors)

Shows opt-in browser integration:

- `WithCORSConfig(...)`
- preflight behavior
- actual CORS response headers
- credentialed origin allowlists

## Advanced integration examples

### [`examples/external-server`](../examples/external-server)

Shows how Servekit composes with pre-existing HTTP infrastructure:

- `WithMux(...)`
- `Handler()`
- externally owned `http.Server`
- explicit `SetReady(...)`

This is one of the most important advanced examples because it proves Servekit does not have to own the full runtime.

### [`examples/advanced-composition`](../examples/advanced-composition)

Shows what a more heavily customized Servekit service can still look like without abandoning the main model:

- custom success and error encoders
- custom `slog` and `http.Server.ErrorLog`
- readiness checks and explicit readiness control
- custom `/healthz`
- CORS config
- telemetry provider and labeling overrides
- global middleware plus route-level overrides
- one raw `HandleHTTP(...)` route inside the same service

This is the late-stage reference example to read when you want to see many advanced hooks composed together in one runnable program.

## Raw HTTP escape hatches

### [`examples/streaming`](../examples/streaming)

Shows why `HandleHTTP` exists for streaming:

- server-sent events
- `http.Flusher`
- long-lived response flow

This is the dedicated example to read if you want to see `http.Flusher` used through Servekit's raw handler path.

### [`examples/reverse-proxy`](../examples/reverse-proxy)

Shows proxy-style raw handler integration:

- `httputil.ReverseProxy`
- request rewriting
- response rewriting
- upstream error handling

### [`examples/response-capture`](../examples/response-capture)

Shows one of the package's subtler implementation details:

- implicit `200` capture when handlers never call `WriteHeader`
- response byte counting for logs and metrics
- preserved `http.Flusher`
- preserved `http.Hijacker`

This example is less about "how to build an app" and more about "why the raw Servekit path is technically credible." It also contains the concrete `http.Hijacker` route in the example set.

## Running examples

Run any example directly from the repository root with:

```bash
go run ./examples/<name>

# for example
go run ./examples/basic
go run ./examples/telemetry
go run ./examples/endpoint-controls
go run ./examples/streaming
```

Each example prints suggested `curl` commands on startup.

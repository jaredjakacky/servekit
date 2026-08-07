# Usage Guide

This guide is the normal Servekit path: `New`, `Handle`, `Run`, plus the small set of route-level controls most services actually need.

The package is opinionated about defaults, but not about architecture. The intended adoption flow is:

1. construct a server with `New`
2. register most routes with `Handle`
3. drop to `HandleHTTP` only when the endpoint truly needs raw response control
4. run the service with `Run`

If that path is enough, Servekit stays small. If it is not, [Advanced Guide](advanced.md) covers the deeper composition hooks, and [API Map](api.md) lists the full exported surface.

## The normal path

Servekit has a strong opinion about what the common case should look like.

### `New`

`New(opts ...Option)` constructs a `Server` with a production-oriented baseline already installed.

A new server starts with:

- JSON response and error encoders
- middleware for panic recovery, OpenTelemetry, request IDs, correlation IDs, and access logs
- built-in `GET /livez`, `GET /readyz`, and `GET /version`
- conservative server timeout values
- a default request body limit

### `Handle`

`Handle` is the default route shape:

```go
s.Handle(http.MethodGet, "/widgets/{id}", func(r *http.Request) (any, error) {
	id := r.PathValue("id")
	return map[string]string{"id": id}, nil
})
```

Use it when the endpoint naturally wants to:

1. inspect the request
2. do application work
3. return one payload or one error

Default behavior:

- `nil` payload -> `204 No Content`
- non-`nil` payload -> `200 OK` with JSON shaped like `{"data": ...}`
- returned error -> delegated to the configured `ErrorEncoder`

If the handler needs explicit HTTP status control, return `servekit.Error(...)` or an `HTTPError`.

The default JSON encoder serializes the complete success body before committing `200 OK`. If serialization fails, Servekit can still return the configured error response. Use `HandleHTTP` when an endpoint needs streaming or other direct response semantics.

Plain Go errors still work. The distinction is:

- a normal error means "this failed"
- `servekit.Error(...)` means "this failed, and here is the HTTP status the client should receive"

That keeps the handler contract simple without forcing every route to write its own error responses.

### `Run`

`Run(ctx)` is the standard lifecycle path. It builds the final handler stack, starts the listener, marks readiness when startup completes unless readiness was set explicitly, and performs graceful shutdown when `ctx` is canceled or the process receives `SIGINT` or `SIGTERM`.

That keeps the service lifecycle small without the caller having to wire shutdown, readiness transitions, and a standard `http.Server` manually.

### Panic behavior

By default, Servekit contains normal panics only while it can still produce a coherent error response.

Servekit installs recovery middleware in commitment-aware mode. By default it:

- logs the panic and stack trace
- writes a best-effort JSON `500` in the default error shape if the response is still uncommitted, including `request_id` when available
- re-panics with `http.ErrAbortHandler` after response commitment so `net/http` aborts the incomplete response or stream
- preserves an incoming `http.ErrAbortHandler` without logging it or attempting a fallback response
- lets access logs and request metrics report the observed outcome

That fallback does not go through a server's custom `ErrorEncoder`; recovery intentionally uses a fixed default JSON error shape.

One nuance matters: access logging and OTel middleware may recover and re-panic internally while the panic unwinds so they can record the request outcome. The outer recovery middleware still decides the final result.

Use `WithPanicPropagation(true)` when every normal panic should use abort-style transport behavior, including panics that occur before response commitment. The default already aborts normal panics after commitment.

Use `WithRecoveryEnabled(false)` only when you truly want panics to escape to the surrounding `net/http` server. In that mode, inner access-log and OTel middleware may still recover and re-panic briefly so they can record the request outcome first.

For the detailed observability behavior around panic requests, see [Observability Guide](observability.md). For raw-route guidance, see [Advanced Guide](advanced.md).

## What a new server gives you

### Middleware order

The built-in handler stack is applied in this order:

1. CORS, when configured
2. panic recovery
3. OpenTelemetry, when enabled
4. request ID
5. correlation ID
6. access log
7. custom middleware added with `WithMiddleware`
8. the mux and endpoint handler

The order matters. For example, request IDs and trace context already exist by the time access logs run.

In request-flow terms, the built-in path looks like:

```text
request
  -> CORS (optional)
  -> Recovery (optional)
  -> OpenTelemetry (optional)
  -> RequestID (optional)
  -> CorrelationID (optional)
  -> AccessLog (optional)
  -> custom WithMiddleware(...)
  -> http.ServeMux route match
  -> route-local timeout / body limit / auth checks
  -> route-local WithEndpointMiddleware(...)
  -> Handle(...) or HandleHTTP(...)
```

That layering is intentional. Global application middleware remains outside route-local policy, while endpoint middleware receives the same authenticated, body-limited, deadline-aware request as the application handler.

Use `WithMiddleware(...)` when behavior should apply across the whole service. Use `WithEndpointMiddleware(...)` when only one matched route needs special policy.

```go
s := servekit.New(
	servekit.WithMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Service", "servekit-example")
			next.ServeHTTP(w, r)
		})
	}),
)

s.Handle(http.MethodGet, "/public", publicHandler)

s.Handle(http.MethodPost, "/admin/publish", publishHandler,
	servekit.WithEndpointMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Route-Scope", "admin")
			next.ServeHTTP(w, r)
		})
	}),
)
```

### Built-in endpoints

By default Servekit mounts:

- `GET /livez`
- `GET /readyz`
- `GET /version`

If you supply `WithHealthHandler(...)`, it also mounts:

- `GET /healthz`

For a composed Kit Series service, pass the shared Opskit registry with
`WithOps(...)`. Then `/readyz` includes Opskit readiness, and
`WithOpsAdmin()` can expose generic read-only component inventory and snapshots.
See [Kit Series Composition](composition.md).

Disable the whole default operational set with `WithDefaultEndpointsEnabled(false)`.

### Routing model

Servekit registers routes onto the underlying `http.ServeMux`.

That means:

- Servekit route patterns follow the standard library mux model
- the package does not introduce a separate router DSL
- advanced users can still integrate with an existing mux through `WithMux(...)`

That is part of the design: Servekit reduces repeated HTTP bootstrap work without hiding the standard library request and routing model.

### Default server settings

New servers start with these conservative settings:

- read timeout: `5s`
- read header timeout: `2s`
- write timeout: `10s`
- idle timeout: `60s`
- max header bytes: `1 MiB`
- shutdown timeout: `15s`
- request body limit: `4 MiB`

These are safer production defaults than a zero-config `http.Server`, not a claim that one fixed set of values is correct for every workload.

## When to use `HandleHTTP`

`HandleHTTP` is the raw escape hatch:

```go
s.HandleHTTP(http.MethodGet, "/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	_, _ = io.WriteString(w, "data: hello\n\n")
	flusher.Flush()
}))
```

Use it when the endpoint needs direct response control, such as:

- streaming or SSE
- reverse proxying
- protocol upgrades or hijacking
- existing third-party handlers
- non-JSON or custom response behavior

This separation between `Handle` and `HandleHTTP` is one of Servekit's core design choices. The package has a short structured path for common API endpoints and a clean raw path for the cases where higher-level encoding is the wrong abstraction.

See [`examples/streaming`](../examples/streaming) for a fuller streaming example using `http.Flusher`.

## Common endpoint controls

Servekit keeps most customization at the route level so the default server setup does not become an all-or-nothing decision.

### Middleware

Use `WithEndpointMiddleware(...)` to wrap only one route.

Use `WithMiddleware(...)` on `New(...)` when the behavior should apply across the whole service instead.

### Timeout

Use `WithEndpointTimeout(...)` to set a request-context timeout for one route. Endpoint middleware and the application handler both receive that context.

### Body limit

Use `WithBodyLimit(...)` to override the server-wide request body limit for one route. Endpoint middleware and the application handler share the limited body. `-1` disables the limit.

### Auth

Use `WithAuthCheck(...)` for simple allow-or-deny behavior that returns HTTP `401`.

Use `WithAuthGate(...)` when the auth layer needs to return a richer or more specific error.

Both auth forms run before endpoint middleware. A rejected request invokes neither endpoint middleware nor the application handler.

### Response encoder override

Use `WithEndpointResponseEncoder(...)` when one `Handle` endpoint should return a different success shape or content type without changing the rest of the server.

### Access log and telemetry suppression

Use `WithSkipAccessLog()` and `WithSkipTelemetry()` for high-frequency or low-value routes such as probes.

See [`examples/endpoint-controls`](../examples/endpoint-controls) for a runnable route-level controls example.

## Common server-level controls

The most important server-wide hooks are:

- `WithAddr(...)`
- `WithLogger(...)`
- `WithMiddleware(...)`
- `WithResponseEncoder(...)`
- `WithErrorEncoder(...)`
- `WithBuildInfo(...)`
- `WithHealthHandler(...)`
- `WithOps(...)`
- `WithReadinessChecks(...)`
- `WithCORSConfig(...)`
- timeout and size options such as `WithReadTimeout(...)` and `WithMaxHeaderBytes(...)`

Use server-level options when the behavior is truly shared. Prefer endpoint options when only one route needs the change.

`WithMiddleware(...)` is the main hook for application-owned cross-cutting policy such as headers, auth context enrichment, or auditing that should run across every route.

## Recommended adoption path

If you are introducing Servekit into a service, the cleanest progression is:

1. start with `New`, `Handle`, and `Run`
2. when composing Kit Series packages, register their Opskit components in one registry and pass it with `WithOps(...)`
3. keep the default JSON encoders unless you have a concrete response contract to enforce
4. use endpoint options for outlier routes instead of immediately replacing global behavior
5. treat `HandleHTTP` as an intentional escape hatch, not as the default style
6. move to the advanced guide only when the service genuinely needs custom encoders, external server ownership, or deeper telemetry control

## Related guides

- [Getting Started](getting-started.md)
- [Kit Series Composition](composition.md)
- [Advanced Guide](advanced.md)
- [Lifecycle and Probes](lifecycle.md)
- [Observability and Middleware](observability.md)
- [API Map](api.md)

# Observability and Middleware

Servekit treats observability as part of the default HTTP stack, not as something every service should rebuild on its own.

A new server gets request IDs, correlation IDs, access logs, panic recovery, request spans, request metrics, and, on the normal `Run(...)` path, connection metrics. The goal is not to replace a team's telemetry stack. It is to make the default service path start from something useful.

## Request IDs

`RequestID()` ensures each request has an `X-Request-ID`.

Behavior:

- if the incoming request already has `X-Request-ID`, Servekit preserves it
- otherwise Servekit generates one
- the value is also attached to the request context

Use `RequestIDFromContext(ctx)` when application code or custom middleware needs that value.

## Correlation IDs

`CorrelationID()` ensures each request has an `X-Correlation-ID`.

Behavior:

- if the incoming request already has `X-Correlation-ID`, Servekit preserves it
- otherwise it reuses the request ID when present
- if neither header exists, it generates a new value

Use `CorrelationIDFromContext(ctx)` when application code needs the correlation ID from request context.

## Access logs

`AccessLog(logger)` emits one structured log entry per completed request.

By default Servekit enables this middleware with the server logger. Logged fields include:

- method
- path
- status
- response bytes
- duration
- matched route
- remote address
- request ID
- correlation ID
- trace ID and span ID when tracing is active

Disable it globally with `WithAccessLogEnabled(false)` or skip it per endpoint with `WithSkipAccessLog()`.

Recovered panic requests still flow through access logs and metrics. If no response headers were committed before the panic, they are reported as `500`. If the handler had already committed a status or body bytes, Servekit keeps the already-observed status because outer recovery cannot safely rewrite the response after that point.

## Panic recovery

`Recovery(logger, propagate)` handles panics inside the HTTP request flow.

The default Servekit path installs `Recovery(..., false)`, so recovered requests do not re-panic unless you opt into propagation explicitly.

One implementation detail is worth knowing: inner observability middleware such as access logging and OTel may recover and re-panic internally while the panic unwinds so they can record logs, spans, and metrics. The outer `Recovery` middleware still decides the final default outcome.

Default mode:

- log the panic and stack trace
- if the response has not been committed yet, write a best-effort JSON `500` in the default error shape, including `request_id` when available
- if the response has already been committed, leave the observed status and body alone
- return normally

Propagation mode:

- still log the panic and stack trace
- re-panic with `http.ErrAbortHandler`
- do not try to write a fallback body

Enable propagation with `WithPanicPropagation(true)` when abort-style transport semantics are more correct than a fallback JSON `500`, such as with streaming or proxy-style handlers.

If recovery is disabled entirely with `WithRecoveryEnabled(false)`, Servekit does not install the outer `Recovery` middleware. Panics still escape to the surrounding server or test harness, although inner observability middleware may briefly recover and re-panic so they can record logs, spans, or metrics.

Recovery's fallback is intentionally fixed rather than routed through a server's configured `ErrorEncoder`. That keeps the generic middleware independent from server-specific response policy while still matching Servekit's default JSON error shape closely.

## OpenTelemetry

Servekit enables built-in OpenTelemetry middleware by default.

Servekit does not just expose hooks for telemetry. The normal path emits request-level tracing and metrics by default, and on the `Run(...)` path it also wires server-level connection metrics.

By default, that middleware:

- extracts incoming trace context
- starts a server span
- records request metadata such as method, route, scheme, and status
- records request metrics
- wires connection metrics on the `Run(...)` path

When explicit providers are not supplied, Servekit uses the global ones.

Available server options:

- `WithOpenTelemetryEnabled(...)`
- `WithTracerProvider(...)`
- `WithMeterProvider(...)`
- `WithPropagator(...)`
- `WithOTelAttributes(...)`
- `WithSpanNameFormatter(...)`
- `WithRouteLabeler(...)`
- `WithOTelPanicMetricEnabled(...)`

Use `TraceIDFromContext(ctx)` and `SpanIDFromContext(ctx)` when application code wants to log or return trace identifiers.

Two common integration modes are:

1. the host application installs global OTel providers and propagators, and Servekit uses those defaults
2. the server overrides providers, propagators, attributes, span naming, or route labels with Servekit options

The repository's [`examples/basic`](../examples/basic) example takes the first path in the smallest possible service. The dedicated [`examples/telemetry`](../examples/telemetry) example goes further and makes both spans and metrics visible through process-wide stdout exporters.

### Request metrics

Servekit's built-in request metrics include:

- `http.server.request.count`
- `http.server.request.duration`
- `http.server.request.in_flight`
- `http.server.request.panic.count`
- `http.server.request.timeout.count`
- `http.server.request.cancellation.count`
- `http.server.request.auth_rejection.count`

`http.server.request.duration` uses second-based histogram buckets with fine
resolution for ordinary request latency and extra buckets at 15s, 30s, and 60s
for slower long-tail requests.

## `Run(...)` versus `Handler()`

The main distinction when choosing between `Run(...)` and `Handler()` is where the metrics come from:

- request IDs, correlation IDs, access logs, request spans, and request metrics live on the handler path
- connection metrics depend on `http.Server.ConnState` and are therefore only wired automatically on the `Run(...)` path

That means mounting `Handler()` into your own `http.Server` still preserves request-level observability, but Servekit does not automatically inject equivalent connection metrics in that external-server mode.

Servekit's built-in connection metrics are:

- `http.server.connection.active`
- `http.server.connection.hijacked.active`

Connection metrics have one extra wrinkle for hijacked connections:

- `http.server.connection.active` covers non-hijacked connections still managed by the normal `net/http` lifecycle
- `http.server.connection.hijacked.active` covers hijacked connections that remain open under handler-owned lifecycle management

That split exists because `http.Server.ConnState` alone is not enough to track the lifetime of a successfully hijacked connection after control leaves the normal server flow.

## CORS

CORS is built in but disabled by default.

Use `WithCORSConfig(...)` when the service must respond to cross-origin browser requests. The config is validated once when the handler stack is built, then applied to preflight and actual requests.

`CORSConfig` lets you control:

- allowed origins
- allowed methods
- allowed headers
- exposed headers
- credentials behavior
- preflight max age

Servekit treats CORS as an application choice, not as a universal default.

## Endpoint-level control

Two endpoint options are especially useful for operational routes:

- `WithSkipAccessLog()`
- `WithSkipTelemetry()`

Servekit uses those internally for built-in probe and version endpoints because high-frequency operational routes often create more noise than value in logs and traces.

## Custom middleware

Use `WithMiddleware(...)` for server-wide middleware and `WithEndpointMiddleware(...)` for route-specific middleware.

Servekit's intended model is not "use our middleware or yours." The built-in stack covers the reusable operational baseline, while application middleware is where service-specific cross-cutting behavior should live.

The two levels are intentionally separate:

- global middleware is for behavior shared across the whole service
- endpoint middleware is for exceptional routes with special policy

That separation keeps customization local when it should be local. A service can stamp headers, enrich auth context, attach auditing behavior, or add one-off route policy without replacing Servekit's request IDs, tracing, access logs, probes, or shutdown model.

Ordering matters here too:

- `WithMiddleware(...)` runs inside Servekit's built-in stack and before mux dispatch
- `WithEndpointMiddleware(...)` wraps only the matched route after routing

See [`examples/endpoint-controls`](../examples/endpoint-controls) for the smallest runnable example that shows both layers, and [`examples/advanced-composition`](../examples/advanced-composition) for the heavier composition path.

## Response capture and raw capability preservation

Servekit wraps the response writer once per request so middleware can observe the final response without buffering or replacing the real write path.

The wrapper tracks:

- committed status code
- response body bytes accepted by the normal response path, including `Write` and the `io.ReaderFrom` fast path when present

Important details:

- the observed status starts at `200` to match `net/http`'s implicit success behavior
- the observed byte count is response-body bytes, not wire-level network traffic
- bytes written after a successful hijack are outside normal `ResponseWriter` observation

The more important reason this wrapper exists is capability preservation. If the underlying writer supports `http.Flusher`, `http.Hijacker`, or `io.ReaderFrom`, Servekit preserves those capabilities so `HandleHTTP(...)` remains a credible raw escape hatch rather than a partially broken one.

## Examples

See these runnable examples:

- [`examples/logging`](../examples/logging) for logger setup, request IDs, and panic behavior
- [`examples/telemetry`](../examples/telemetry) for the focused tracing, request-metrics, and connection-metrics story
- [`examples/endpoint-controls`](../examples/endpoint-controls) for global and route-local custom middleware
- [`examples/cors`](../examples/cors) for opt-in browser CORS behavior
- [`examples/response-capture`](../examples/response-capture) for status capture, byte counting, `Flush`, and `Hijack`
- [`examples/streaming`](../examples/streaming) for the raw streaming path

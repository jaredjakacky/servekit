# Advanced Guide

Use this guide when `New`, `Handle`, and `Run` are still the right model, but the service needs deeper control.

These are escape hatches, not the default path. If the service can stay on `New`, `Handle`, and `Run`, it usually should.

This guide is intentionally selective. It focuses on advanced composition patterns and the deeper hooks that change how a service is put together. For the full exported surface, see [API Map](api.md). For detailed lifecycle, observability, and route-level behavior, use the companion guides where those topics fit more naturally.

## When you are in advanced territory

You are probably beyond the normal path when you need one or more of these:

- a custom success or error response contract
- an existing `http.ServeMux` or pre-built route tree
- your own `http.Server` lifecycle
- explicit readiness ownership
- custom logging for `http.Server.ErrorLog`
- custom OpenTelemetry providers, propagators, attributes, or route labels
- opt-in browser CORS behavior
- streaming, proxying, or raw response semantics where panic handling and write timeouts matter
- selective disabling or replacement of built-in defaults

## Prefer route-level controls first

Many services do not need a new composition model. They just need one route to behave differently.

Start with endpoint options such as:

- `WithEndpointTimeout(...)`
- `WithBodyLimit(...)`
- `WithAuthCheck(...)` / `WithAuthGate(...)`
- `WithEndpointMiddleware(...)`
- `WithEndpointResponseEncoder(...)`
- `WithSkipAccessLog()` / `WithSkipTelemetry()`

Those hooks usually let the service stay on the normal Servekit path while still handling outlier routes cleanly. See [Usage Guide](usage.md) for the full route-level model.

## Advanced composition patterns

These are reference patterns, not the recommended starting point. Use them when the service genuinely needs several advanced hooks composed together.

If you want one runnable all-up reference service rather than smaller patterns, see [`examples/advanced-composition`](../examples/advanced-composition).

### House response contract with one route-specific exception

Use this pattern when the service has a standard response contract but one route needs a different success shape or content type.

```go
s := servekit.New(
	servekit.WithResponseEncoder(mySuccessEncoder),
	servekit.WithErrorEncoder(myErrorEncoder),
)

s.Handle(http.MethodGet, "/widgets", listWidgets)

s.Handle(
	http.MethodGet,
	"/widgets/export",
	exportWidgets,
	servekit.WithEndpointResponseEncoder(csvEncoder),
)
```

What this buys you:

- one server-wide success contract
- one server-wide error contract
- a narrow escape hatch for an outlier route instead of a global exception

See [`examples/custom-encoding`](../examples/custom-encoding) for the runnable version.

### Existing mux plus external `http.Server`

Use this pattern when another part of the application needs to own transport settings or server lifecycle, but you still want Servekit to own the wrapped handler stack and built-in routes.

```go
mux := http.NewServeMux()
mux.Handle("GET /legacy", legacyHandler)

s := servekit.New(
	servekit.WithMux(mux),
)

s.Handle(http.MethodGet, "/ready", readyHandler)

server := &http.Server{
	Addr:              ":8080",
	Handler:           s.Handler(),
	ReadTimeout:       5 * time.Second,
	ReadHeaderTimeout: 2 * time.Second,
	WriteTimeout:      10 * time.Second,
	IdleTimeout:       60 * time.Second,
}
```

Use `WithMux(...)` when the application already has an `http.ServeMux` and you want Servekit to register its routes into that mux.

Use `Handler()` when another part of the application owns the outer `http.Server` lifecycle and you want Servekit to provide the wrapped handler stack only.

What this buys you:

- Servekit middleware, probes, and route registration
- application-owned transport and shutdown policy
- explicit readiness ownership through `SetReady(...)`

When you own the outer `http.Server`, readiness becomes your responsibility. In that mode:

1. build the handler with `Handler()`
2. start your own server
3. call `SetReady(true)` only when the service is actually ready
4. call `SetReady(false)` before beginning your own shutdown

See [`examples/external-server`](../examples/external-server) for the runnable version.

### Customized telemetry and logging baseline

Use this pattern when the host service owns the telemetry SDK and logging policy, but you still want Servekit's default request lifecycle behavior around it.

```go
s := servekit.New(
	servekit.WithLogger(appLogger),
	servekit.WithHTTPServerErrorLog(httpServerLog),
	servekit.WithTracerProvider(tp),
	servekit.WithMeterProvider(mp),
	servekit.WithPropagator(propagator),
	servekit.WithOTelAttributes(extraAttrs),
	servekit.WithSpanNameFormatter(spanNamer),
	servekit.WithRouteLabeler(routeLabeler),
)
```

What this buys you:

- one application-owned logging policy
- one application-owned OTel SDK configuration
- Servekit request middleware and lifecycle behavior on top of those providers

See [`examples/logging`](../examples/logging) for the logging side and [Observability and Middleware](observability.md) for the telemetry side.

## Raw responses, recovery, and long-lived routes

Ordinary request-response handlers usually want the default recovery behavior:

- log the panic
- write a best-effort JSON `500` if the response is still uncommitted
- keep the failure visible without crashing the process

Long-lived or raw-response endpoints can have different needs.

Relevant hooks:

- `HandleHTTP(...)`
- `WithRecoveryEnabled(...)`
- `WithPanicPropagation(true)`
- `WithWriteTimeout(...)`

For streaming, SSE, reverse proxying, and other long-lived responses:

- `HandleHTTP(...)` is usually the right route API
- `WithPanicPropagation(true)` may be more correct than writing a fallback JSON `500`
- `WithWriteTimeout(0)` or another workload-specific value may be necessary if the response is intentionally long-lived

In propagation mode, recovery still logs the original panic and stack trace, but it re-panics with `http.ErrAbortHandler` instead of trying to write a fallback body.

If recovery is disabled with `WithRecoveryEnabled(false)`, Servekit does not install the outer recovery middleware. Panics still escape to the surrounding server behavior, although inner access-log and OTel middleware may briefly recover and re-panic so they can record the request outcome first.

There is also a real timeout-composition concern here. For proxy-style routes, several timing layers can all matter at once:

- Servekit server timeouts on the client-facing connection
- endpoint-level timeout policy inside the route
- proxy `Transport` settings on the upstream connection

Those layers are separate and can conflict. For raw streaming and proxy routes, choose server settings, endpoint settings, and transport settings together rather than in isolation.

`HandleHTTP(...)` also preserves raw writer capabilities such as `http.Flusher`, `http.Hijacker`, and `io.ReaderFrom` when the underlying writer supports them.

See [`examples/streaming`](../examples/streaming), [`examples/reverse-proxy`](../examples/reverse-proxy), and [`examples/response-capture`](../examples/response-capture).

## Logging and transport-level logging

Servekit's main logger and `http.Server.ErrorLog` are related, but not identical.

Relevant hooks:

- `WithLogger(...)`
- `WithHTTPServerErrorLog(...)`
- `WithHTTPServerErrorLogLevel(...)`

The normal path is to set `WithLogger(...)` and let `Run(...)` derive `http.Server.ErrorLog` from that logger's handler.

Use `WithHTTPServerErrorLog(...)` only when you need explicit control over transport-level or accept-loop logging, separate from the rest of the application's structured request logs.

`http.Server.ErrorLog` is an old stdlib `*log.Logger`, not a `*slog.Logger`. When you do not provide one explicitly, Servekit bridges from the configured slog handler using `slog.NewLogLogger(...)`. That means the derived transport logger can reuse the same destination and formatting while still being tagged at a chosen severity level.

See [`examples/logging`](../examples/logging) for the recommended pattern.

## OpenTelemetry customization

Servekit ships with OTel enabled by default, but it does not force a fixed telemetry policy.

Available hooks:

- `WithOpenTelemetryEnabled(...)`
- `WithTracerProvider(...)`
- `WithMeterProvider(...)`
- `WithPropagator(...)`
- `WithOTelAttributes(...)`
- `WithSpanNameFormatter(...)`
- `WithRouteLabeler(...)`
- `WithOTelPanicMetricEnabled(...)`

These are useful when:

- the host application installs its own providers
- span names should follow a house convention
- routes need a custom low-cardinality label strategy
- extra request attributes belong on spans and metrics
- panic counting should be enabled or disabled explicitly

The important distinction:

- request-level tracing and metrics live on the handler path and still work when you use `Handler()`
- connection metrics are only wired automatically on the `Run(...)` path because they depend on `http.Server.ConnState`

That distinction is covered again in [Observability and Middleware](observability.md).

## Transport and shutdown tuning

Servekit's defaults are intentionally conservative, but advanced services sometimes need explicit transport tuning.

Relevant hooks:

- `WithReadTimeout(...)`
- `WithReadHeaderTimeout(...)`
- `WithWriteTimeout(...)`
- `WithIdleTimeout(...)`
- `WithMaxHeaderBytes(...)`
- `WithRequestBodyLimit(...)`
- `WithShutdownTimeout(...)`
- `WithShutdownDrainDelay(...)`

Common reasons to tune these:

- long-lived streaming or proxy responses need a different write-timeout strategy
- unusually large headers or bodies need explicit sizing policy
- shutdown behavior needs a different drain delay or graceful-shutdown budget

The default values and the probe/shutdown lifecycle are covered in [Usage Guide](usage.md) and [Lifecycle and Probes](lifecycle.md). The advanced point here is that these settings interact, so they should be chosen together instead of one by one.

## CORS as application policy

CORS is built in, but disabled by default.

Use `WithCORSConfig(...)` when the service must answer browser preflight and actual cross-origin requests. The config is validated once when the handler stack is built and then applied by middleware.

Why this is an advanced feature rather than the default:

- many services never need browser CORS at all
- credentialed CORS has correctness constraints
- origin policy is application-specific, not universal

See [`examples/cors`](../examples/cors) for a runnable example.

## Selective default toggles

Servekit's defaults are intended to be useful, but they are not sacred.

Important toggles include:

- `WithDefaultEndpointsEnabled(...)`
- `WithRecoveryEnabled(...)`
- `WithAccessLogEnabled(...)`
- `WithRequestIDEnabled(...)`
- `WithCorrelationIDEnabled(...)`
- `WithOpenTelemetryEnabled(...)`

The practical recommendation is to disable defaults only when there is a concrete reason, not just because customization exists.

## Out of scope for v1

Some useful adjacent concerns are deliberately out of scope for now.

That is intentional. Those concerns usually become topology-, identity-, or product-specific quickly. Servekit tries to own the reusable HTTP bootstrap layer, not every policy concern surrounding an HTTP service.

### Rate limiting is out of scope for v1

Servekit v1 does not include built-in rate limiting.

Rate limiting becomes policy-heavy very quickly:

- what is being limited: IP, user, API key, route, tenant, or whole process load
- where caller identity comes from: direct socket, ingress headers, gateway metadata, or service mesh context
- whether limits are local or distributed
- what fairness or tenancy model the service expects

Those choices depend heavily on deployment topology and product requirements. For v1, Servekit does not try to own that policy.

### Cookie and CSRF policy are out of scope for v1

Servekit v1 does not include built-in cookie or session management, and it does not define a built-in CSRF policy.

That is intentional. CSRF protection depends on the application's authentication model:

- whether the service uses cookies at all
- whether cross-site cookies are required
- whether `SameSite` should be `Lax`, `Strict`, or `None`
- whether protection should rely on origin checks, CSRF tokens, or both

Servekit stays on the `net/http` model, so applications can still use cookies and add CSRF protection in their own handlers or middleware. V1 simply does not define one built-in session or CSRF policy for every service.

### Backpressure and load shedding are out of scope for v1

Servekit v1 does not include built-in request queueing, backpressure controls, or load-shedding policy.

There is a legitimate narrow case for a small in-process concurrency valve, but that design is intentionally deferred rather than rushed into v1. Once queueing or overload controls are added, Servekit would need to define behavior such as:

- what is limited
- what happens when the limit is reached
- whether requests wait or fail fast
- how long-lived requests, probes, and cancellations behave

When a service needs those behaviors, the recommendation is to rely on application-owned middleware, dedicated libraries, or infrastructure controls instead of expecting one built-in policy from Servekit.

## Recommended advanced sequence

If the service is moving beyond the normal path, the cleanest progression is:

1. keep the default route model and add per-endpoint controls first
2. customize response encoders only when the service has a stable contract to enforce
3. install house logging and telemetry providers only when the application genuinely owns those policies
4. move to `Handler()` and your own `http.Server` only when another component truly needs to own lifecycle
5. treat raw streaming and proxy behavior as special cases, not as the default route style

## Related material

- [Usage Guide](usage.md)
- [Lifecycle and Probes](lifecycle.md)
- [Observability and Middleware](observability.md)
- [API Map](api.md)
- [Examples Guide](examples.md)

# API Map

This is the fast way to orient yourself in Servekit's public API.

It covers the full exported surface of the `servekit` package and the `version` subpackage.

Go doc comments remain the canonical symbol-level reference. This file is the companion view that groups the exported surface by the decisions you make when using the package.

If you only remember the common path, remember this:

- `New(...)` creates the server with Servekit's baseline already installed
- `Handle(...)` is the normal route shape for request in, payload/error out
- `Run(...)` listens, mounts the built-in operational endpoints, manages readiness, and shuts down cleanly

Everything else in this file exists to customize that path without abandoning `net/http`.

## Package `servekit`

### Start here

- `Server`

  The main service object. It owns route registration, built-in middleware, readiness state, transport settings, and the default operational endpoints.

- `New(...)`

  Creates a server with Servekit's production-oriented baseline: JSON success and error encoding, panic recovery, request and correlation IDs, access logs, OpenTelemetry, built-in `GET /livez`, `GET /readyz`, and `GET /version`, plus conservative `http.Server` timeouts and a default request body limit.

- `Option`

  Server-wide configuration hook applied in order during `New(...)`.

### Normal route path

- `Handle(...)`

  Registers the normal Servekit route shape. Use this when the endpoint wants to inspect the request, do application work, and return one payload or one error.

- `HandlerFunc`

  Structured handler function used by `Handle(...)`.

  Shape: `func(*http.Request) (any, error)`

- `ResponseEncoder`

  Success encoder used by `Handle(...)`.

  Shape: `func(http.ResponseWriter, *http.Request, any) error`

- `ErrorEncoder`

  Error encoder used by `Handle(...)`.

  Shape: `func(http.ResponseWriter, *http.Request, error) error`

- `JSONResponse()`

  Default success encoder. `nil` payloads become `204 No Content`. Non-`nil` payloads become `200 OK` with JSON shaped like `{"data": ...}`.

- `JSONError()`

  Default error encoder. It understands `HTTPError`, timeouts, cancellations, and request-body-limit failures and renders a JSON error response.

### Raw route path

- `HandleHTTP(...)`

  Registers a raw `http.Handler` route. Use this when the endpoint needs direct response control for streaming, SSE, proxying, upgrades, hijacking, or existing handler reuse.

- `EndpointOption`

  Route-level configuration hook used by both `Handle(...)` and `HandleHTTP(...)`. The config type itself is internal; callers use the exported `With...` endpoint helpers below rather than constructing endpoint options directly.

### Lifecycle and readiness

- `Run(...)`

  Standard lifecycle path. It listens on the configured address, builds the handler stack, marks readiness on startup unless readiness was set explicitly, and performs graceful shutdown on context cancellation or `SIGINT` / `SIGTERM`.

- `Handler()`

  Builds the final wrapped handler stack without starting a listener. Use this when another component owns the outer `http.Server`.

- `SetReady(...)`

  Sets the readiness state exposed by `/readyz` and opts the server into explicit readiness ownership.

- `Ready()`

  Returns the current readiness state.

- `ReadinessCheck`

  Dependency check hook used by the built-in `/readyz` endpoint. Returning an error marks the service not ready.

  Shape: `func(context.Context) error`

### Error model

- `HTTPError`

  Error value for handlers that need explicit HTTP status control instead of the default error mapping.

- `HTTPError.StatusCode`

  The HTTP status Servekit should return for the error.

- `HTTPError.Message`

  The client-facing error text.

- `HTTPError.Err`

  The wrapped underlying cause, when present.

- `HTTPError.Error()`

  Implements the `error` interface.

- `HTTPError.Unwrap()`

  Exposes the wrapped cause for `errors.Is(...)` and `errors.As(...)`.

- `Error(...)`

  Convenience constructor for `HTTPError`.

### Middleware building blocks

- `Middleware`

  Standard middleware shape used throughout Servekit.

  Shape: `func(http.Handler) http.Handler`

- `Chain(...)`

  Applies middleware in declaration order.

### Built-in middleware and context helpers

- `RequestID()`

  Ensures the request has an `X-Request-ID` header and stores that value in request context.

- `CorrelationID()`

  Ensures the request has an `X-Correlation-ID` header and stores that value in request context.

- `AccessLog(...)`

  Emits one structured log entry per completed request.

- `SkipAccessLog()`

  Marks a request so `AccessLog(...)` omits it.

- `Recovery(...)`

  Panic recovery middleware. In default mode it logs the panic and writes a best-effort JSON `500` when possible. In propagation mode it logs the panic and re-panics with `http.ErrAbortHandler` instead of writing a fallback body.

- `RequestIDFromContext(...)`

  Returns the request ID stored in request context.

- `CorrelationIDFromContext(...)`

  Returns the correlation ID stored in request context.

- `TraceIDFromContext(...)`

  Returns the active trace ID from request context when tracing is active.

- `SpanIDFromContext(...)`

  Returns the active span ID from request context when tracing is active.

### Server options

#### Transport and lifecycle

- `WithAddr(addr string)`

  Sets the TCP listen address used by `Run(...)`. Default: `:8080`.

- `WithReadTimeout(timeout time.Duration)`

  Sets `http.Server.ReadTimeout`. Default: `5s`.

- `WithReadHeaderTimeout(timeout time.Duration)`

  Sets `http.Server.ReadHeaderTimeout`. Default: `2s`.

- `WithWriteTimeout(timeout time.Duration)`

  Sets `http.Server.WriteTimeout`. Default: `10s`. Streaming and proxy routes often need a different value or zero.

- `WithIdleTimeout(timeout time.Duration)`

  Sets `http.Server.IdleTimeout`. Default: `60s`.

- `WithMaxHeaderBytes(n int)`

  Sets `http.Server.MaxHeaderBytes` when `n > 0`. Default: `1 MiB`.

- `WithShutdownTimeout(timeout time.Duration)`

  Sets the timeout used for graceful shutdown. Default: `15s`.

- `WithShutdownDrainDelay(delay time.Duration)`

  Waits after readiness flips false and before shutdown begins, giving load balancers time to observe `/readyz`.

#### Logging and server internals

- `WithLogger(logger *slog.Logger)`

  Sets the logger used by built-in middleware and server internals.

- `WithHTTPServerErrorLog(logger *log.Logger)`

  Advanced override for `http.Server.ErrorLog`.

- `WithHTTPServerErrorLogLevel(level slog.Level)`

  Sets the slog level used when Servekit derives `http.Server.ErrorLog` from the server logger's handler.

#### Composition and routing

- `WithMux(mux *http.ServeMux)`

  Replaces the underlying mux. Use this when the service already has an `http.ServeMux` and wants Servekit to register into it.

- `WithMiddleware(mw ...Middleware)`

  Appends global middleware to the server-wide handler stack.

#### Response model and build metadata

- `WithResponseEncoder(encoder ResponseEncoder)`

  Replaces the default success encoder used by `Handle(...)`.

- `WithErrorEncoder(encoder ErrorEncoder)`

  Replaces the default error encoder used by `Handle(...)`.

- `WithBuildInfo(version, commit, date string)`

  Overrides the version metadata served by the built-in `/version` endpoint.

#### Operational endpoints and readiness

- `WithHealthHandler(handler http.Handler)`

  Mounts a user-defined `GET /healthz` endpoint.

- `WithReadinessChecks(checks ...ReadinessCheck)`

  Appends checks evaluated by the built-in `/readyz` endpoint.

- `WithDefaultEndpointsEnabled(enabled bool)`

  Enables or disables the built-in operational endpoints: `GET /livez`, `GET /readyz`, `GET /version`, and `GET /healthz` when configured.

#### Request limits and defaults

- `WithRequestBodyLimit(n int64)`

  Sets the default request-body limit for all endpoints. Default: `4 MiB`. Use `-1` to disable the limit globally.

#### Built-in middleware toggles

- `WithRecoveryEnabled(enabled bool)`

  Enables or disables Servekit's outer panic recovery middleware.

- `WithPanicPropagation(enabled bool)`

  Switches recovery between default contain-and-continue mode and abort-style propagation with `http.ErrAbortHandler`.

- `WithAccessLogEnabled(enabled bool)`

  Enables or disables access-log middleware.

- `WithRequestIDEnabled(enabled bool)`

  Enables or disables request ID middleware.

- `WithCorrelationIDEnabled(enabled bool)`

  Enables or disables correlation ID middleware.

- `WithOpenTelemetryEnabled(enabled bool)`

  Enables or disables built-in OpenTelemetry tracing and metrics middleware. On the normal `Run(...)` path, that also controls Servekit's server connection metrics.

#### OpenTelemetry customization

Servekit's default path already includes request-level tracing and metrics. On the `Run(...)` path it also records server connection metrics. These options exist for teams that want to replace or shape that telemetry policy.

- `WithTracerProvider(tp trace.TracerProvider)`

  Sets the tracer provider used by Servekit's tracing middleware. When unset, Servekit uses `otel.GetTracerProvider()`.

- `WithMeterProvider(mp metric.MeterProvider)`

  Sets the meter provider used by Servekit's metrics middleware. When unset, Servekit uses `otel.GetMeterProvider()`.

- `WithPropagator(p propagation.TextMapPropagator)`

  Sets the text-map propagator used to extract incoming trace context. When unset, Servekit uses `otel.GetTextMapPropagator()`.

- `WithOTelAttributes(fn func(*http.Request) []attribute.KeyValue)`

  Appends request-derived attributes to spans and metrics.

- `WithSpanNameFormatter(fn func(*http.Request, string) string)`

  Overrides span naming.

- `WithRouteLabeler(fn func(*http.Request) string)`

  Overrides the low-cardinality route label used for spans and metrics.

- `WithOTelPanicMetricEnabled(enabled bool)`

  Enables or disables panic counter metrics.

#### CORS

- `WithCORSConfig(cfg CORSConfig)`

  Opts into Servekit's built-in CORS middleware. CORS is disabled by default.

### Route-level controls

- `WithEndpointMiddleware(mw ...Middleware)`

  Appends middleware for one route only.

- `WithEndpointTimeout(timeout time.Duration)`

  Applies a request-context timeout to one route.

- `WithBodyLimit(n int64)`

  Overrides the server-wide request-body limit for one route. Use `-1` to disable the limit for that route.

- `WithAuthCheck(check func(*http.Request) bool)`

  Adds a simple allow-or-deny auth gate. When it returns false, Servekit responds with HTTP `401`.

- `WithAuthGate(fn func(*http.Request) error)`

  Adds an error-returning auth gate for richer or more specific auth failure behavior.

- `WithEndpointResponseEncoder(encoder ResponseEncoder)`

  Overrides success encoding for one `Handle(...)` route without changing the rest of the server.

- `WithSkipAccessLog()`

  Suppresses access-log output for one route.

- `WithSkipTelemetry()`

  Suppresses built-in tracing and metrics for one route.

### CORS model

- `CORSConfig`

  Configures Servekit's opt-in browser CORS middleware.

- `CORSConfig.AllowedOrigins []string`

  Exact origin allowlist in `scheme://host[:port]` form. When empty and credentials are disabled, Servekit allows all origins.

- `CORSConfig.AllowedMethods []string`

  Allowlist returned on successful preflight responses. When empty, Servekit uses a conservative default set of common HTTP methods.

- `CORSConfig.AllowedHeaders []string`

  Allowlist for `Access-Control-Request-Headers` on preflight requests. When empty, Servekit uses a small default set.

- `CORSConfig.ExposedHeaders []string`

  Response headers browsers may expose to calling JavaScript on successful cross-origin requests.

- `CORSConfig.AllowCredentials bool`

  Enables `Access-Control-Allow-Credentials: true`. When true, `AllowedOrigins` must be explicit and must not contain `"*"`.

- `CORSConfig.MaxAge int`

  Controls `Access-Control-Max-Age` on successful preflight responses. `0` uses Servekit's default of `600` seconds. Negative disables the header.

## Package `version`

The `version` subpackage keeps build metadata reusable outside the server itself.

### Build metadata variables

- `Version`

  The application or module version. Usually set with `-ldflags`, but Servekit also attempts to populate it from Go build info for local builds.

- `Commit`

  The short VCS revision, when available.

- `Date`

  The build or VCS timestamp, when available.

### Version payload type

- `Info`

  Serialized build metadata payload used by the built-in `/version` endpoint and reusable elsewhere in the application.

- `Info.Version string`

  Application or module version.

- `Info.Commit string`

  Short VCS revision.

- `Info.Date string`

  Build or VCS timestamp.

- `Info.GoVersion string`

  Go runtime version for the running binary.

- `Get()`

  Snapshots the current build metadata variables into an `Info` value.

- `Info.String()`

  Formats `Info` as a compact single-line summary.

- `Info.Handler()`

  Returns an `http.Handler` that serves the `Info` value as JSON.

## Suggested reading order

If you are new to the codebase:

1. [README](../README.md)
2. [Getting Started](getting-started.md)
3. [Usage Guide](usage.md)
4. [API Map](api.md)
5. [Advanced Guide](advanced.md)
6. [Lifecycle and Probes](lifecycle.md)
7. [Observability and Middleware](observability.md)
8. [Examples Guide](examples.md)

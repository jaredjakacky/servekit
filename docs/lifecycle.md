# Lifecycle and Probes

Servekit treats lifecycle as part of the normal server path, not as glue around the handler. If you use `Run(...)`, you get probes, readiness transitions, signal handling, and graceful shutdown together.

## Built-in endpoints

By default, Servekit mounts:

- `GET /livez`
- `GET /readyz`
- `GET /version`

If you provide `WithHealthHandler(...)`, it also mounts:

- `GET /healthz`

These routes are operational defaults. They do not replace application-owned routes.

`/version` in particular describes the running service binary's build metadata as exposed through Servekit's version package. It is not a runtime statement about a separately installed Servekit library release.

## `/livez`

`/livez` answers the narrow question: is this process alive enough to respond?

It returns `200 OK` with a small JSON body when the process is up.

## `/readyz`

`/readyz` answers the more important traffic question: should this instance receive requests right now?

A newly constructed `Server` starts not ready.

Default behavior:

- before readiness is true, `/readyz` returns `503 Service Unavailable`
- once the server is ready, `/readyz` returns `200 OK`
- if readiness checks fail, `/readyz` returns `503 Service Unavailable` with a `reason`

## Readiness checks

Use `WithReadinessChecks(...)` to append dependency checks that `/readyz` runs once the server is otherwise marked ready.

Each `ReadinessCheck` returns:

- `nil` when that dependency is ready
- a non-`nil` error when it is not

If any check fails, Servekit reports the service as not ready and includes the error text in the JSON response.

## `SetReady`

`SetReady(...)` gives the application explicit control over readiness state.

Calling it has two effects:

1. it changes the readiness value exposed by `/readyz`
2. it opts the server into explicit readiness control, so `Run(...)` no longer forces readiness to true during startup

Use this when your service has warmup work, cache priming, data sync, or other startup sequencing that must complete before traffic is safe.

## `Run`

`Run(ctx)` is the full lifecycle path:

1. build the final handler stack
2. start the listener
3. mark readiness true unless readiness was already set explicitly
4. serve requests until shutdown starts
5. on shutdown, mark readiness false and call `http.Server.Shutdown`

`Run` also listens for `SIGINT` and `SIGTERM`, so the common `main` path can stay small.

## Graceful shutdown

Servekit supports two shutdown tuning knobs:

- `WithShutdownTimeout(...)`
- `WithShutdownDrainDelay(...)`

Shutdown behavior is:

1. readiness flips false
2. optional drain delay waits so upstream load balancers can observe `/readyz` go false
3. graceful shutdown begins with the configured timeout

This pattern is especially useful in containerized or load-balanced environments.

## `/healthz`

Servekit deliberately does not impose a built-in health model beyond `/livez` and `/readyz`.

If your service wants a richer application-specific health endpoint, supply one with `WithHealthHandler(...)`. That keeps the default operational probe story intact while still leaving room for service-specific detail.

## External `http.Server` ownership

If you do not use `Run(...)` and instead mount `Handler()` into your own `http.Server`, the lifecycle behavior is still valid, but readiness becomes your responsibility.

In that setup:

- build the handler with `Handler()`
- start your own server however you like
- call `SetReady(true)` only when the service is actually ready
- call `SetReady(false)` before your own shutdown flow begins

## Examples

See [`examples/readiness`](../examples/readiness) for a runnable example that combines:

- explicit dependency readiness checks
- a custom `/healthz`
- drain delay on shutdown
- a slow endpoint that respects request cancellation

See [`examples/external-server`](../examples/external-server) for the advanced case where Servekit does not own the outer `http.Server` and readiness is managed explicitly with `SetReady(...)`.

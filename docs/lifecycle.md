# Lifecycle and Probes

Servekit treats lifecycle as part of the normal server path, not as glue around the handler. If you use `Run(...)`, you get probes, readiness transitions, signal handling, and graceful shutdown together.

## Built-in endpoints

By default, Servekit mounts:

- `GET /livez`
- `GET /readyz`
- `GET /version`

If you provide `WithHealthHandler(...)`, it also mounts:

- `GET /healthz`

If you provide `WithOps(..., WithOpsAdmin())`, Servekit also mounts read-only Opskit component admin routes:

- `GET /admin/components`
- `GET /admin/components/{name}`

The probe and version routes are operational defaults. Admin routes are opt-in. They do not replace application-owned routes.

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
- if `WithOps(...)` is configured and Opskit readiness is not ready, `/readyz` returns `503 Service Unavailable` with Opskit readiness details
- if a standalone readiness predicate fails, `/readyz` returns `503 Service Unavailable` with a `reason`

## Opskit readiness

Use `WithOps(...)` when your service has an Opskit registry that represents component readiness.

For composed Kit Series services, this is the primary readiness path. The
application registers components from sibling kits in one Opskit registry and
Servekit receives only that registry; it does not integrate with individual
domain kits directly. See [Kit Series Composition](composition.md).

Servekit still owns the HTTP probe and lifecycle gate. When `WithOps(...)` is configured, `/readyz` evaluates readiness in this order:

1. Servekit lifecycle readiness: startup, shutdown, drain delay, and explicit `SetReady(...)`.
2. Opskit registry readiness.
3. `WithReadinessChecks(...)` predicates.

Opskit readiness is only evaluated after Servekit's own readiness is true. That keeps shutdown, drain delay, and explicit `SetReady(...)` behavior authoritative at the HTTP service layer.

Opskit readiness and component snapshot reads receive a bounded context. The default timeout is `2s`; override it with `WithOpsTimeout(...)` when your components need a different probe or snapshot budget. This supplies a deadline but cannot force a component method to return when that implementation ignores context cancellation.

When Opskit is not ready, the response includes its aggregate readiness view:

```json
{
  "status": "not_ready",
  "reason": "one or more readiness components are not ready",
  "readiness": {
    "ready": false,
    "reason": "one or more readiness components are not ready"
  }
}
```

Readiness reasons and component details must be safe for the audience that can
reach `/readyz`.

## Readiness checks

Use `WithReadinessChecks(...)` to append lightweight readiness predicates that `/readyz` evaluates once the server is otherwise marked ready.

Each `ReadinessCheck` returns:

- `nil` when that dependency is ready
- a non-`nil` error when it is not

If any check fails, Servekit reports the service as not ready and includes the error text in the JSON response.

`WithReadinessChecks(...)` remains the standalone readiness hook for small services that do not need an Opskit registry. When `WithOps(...)` is configured, these predicates run after Opskit readiness succeeds. They should read fast local or cached state; they should not perform network probes or expensive active work. For composed Kit Series services, prefer cached component readiness through Opskit and run active dependency checks outside the probe path.

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

## Opskit admin routes

`WithOpsAdmin()` opts into read-only component admin routes backed by the configured Opskit registry:

- `GET /admin/components` returns passive registry component inventory without evaluating component state.
- `GET /admin/components/{name}` returns one component snapshot.

These routes present passive Opskit state only. Servekit does not run checks, dispatch commands, or execute other active Opskit capabilities.

The component snapshot route may evaluate the component's `Status`,
`Readiness`, and `Inspect` methods. Those methods are passive by contract and
should return local or cached operational state. Inspection results and errors
must be safe to serialize to the admin audience.

These routes are controlled by `WithOpsAdmin()`, not by `WithDefaultEndpointsEnabled(...)`.

Opskit admin routes are not authenticated by default. Production services should protect them with `WithOpsAdminAuthGate(...)` or equivalent network-level controls.

Use `WithOpsAdminAuthGate(...)` alongside `WithOpsAdmin()` to require an auth gate for these routes. The auth gate configures protection only; it does not expose admin routes by itself.

## External `http.Server` ownership

If you do not use `Run(...)` and instead mount `Handler()` into your own `http.Server`, the lifecycle behavior is still valid, but readiness becomes your responsibility.

In that setup:

- build the handler with `Handler()`
- start your own server however you like
- call `SetReady(true)` only when the service is actually ready
- call `SetReady(false)` before your own shutdown flow begins

## Examples

See [`examples/operations`](../examples/operations) for the primary composed
service path using Opskit-backed readiness, authenticated component inventory,
and component snapshots without importing any domain kit.

See [`examples/readiness`](../examples/readiness) for a runnable example that combines:

- standalone local readiness predicates for a service without Opskit
- a custom `/healthz`
- drain delay on shutdown
- a slow endpoint that respects request cancellation

See [`examples/external-server`](../examples/external-server) for the advanced case where Servekit does not own the outer `http.Server` and readiness is managed explicitly with `SetReady(...)`.

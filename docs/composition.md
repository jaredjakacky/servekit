# Kit Series Composition

Servekit owns HTTP bootstrap and presentation. Opskit owns the shared
operations registry. Applications compose the two by registering operational
components with Opskit and giving that one registry to Servekit.

This is the primary composition path for a service built from multiple Kit
Series packages:

```go
ops := opskit.NewRegistry()

// The application composition root registers components here. Components may
// come from sibling kits or from application code; Servekit does not need to
// know which package produced them.
ops.MustRegister(database, opskit.Required())
ops.MustRegister(buildInfo, opskit.Informational())

server := servekit.New(
	servekit.WithOps(
		ops,
		servekit.WithOpsAdmin(),
		servekit.WithOpsAdminAuthGate(requireAdmin),
	),
)
```

The resulting ownership stays narrow:

| Concern | Owner |
| --- | --- |
| HTTP listener, routing, encoding, and shutdown | Servekit |
| HTTP lifecycle readiness gate | Servekit |
| Shared component registration and readiness aggregation | Opskit |
| Component status, readiness, and inspection data | The registered component |
| Check scheduling and execution | Workerkit or application runtime |
| Command authorization and dispatch | Application control plane |

Servekit receives only `*opskit.Registry`. It does not import Configkit,
Workerkit, Dependkit, Clientkit, or any other domain kit. A sibling kit joins
the HTTP operations surface by implementing Opskit contracts and being
registered by the application.

## Canonical current-kit example

[`examples/kit-series-composition`](../examples/kit-series-composition) is the
canonical single-service composition for the currently available kits. Its
application composition root:

- loads a typed Configkit manager
- registers a Dependkit registry as the required cached dependency signal
- gives that registry explicitly to a Workerkit check-group loop through
  Dependkit's released Opskit adapter
- registers the Configkit manager, Dependkit registry, and Workerkit runtime in
  one Opskit registry
- gives only that Opskit registry to Servekit
- starts and shuts down Workerkit explicitly around the Servekit server

The example does not mount Workerkit-specific command dispatch or lifecycle
routes. Generic `/readyz` and `/admin/components` requests remain passive;
focused Workerkit examples cover active HTTP controls separately.

The sibling modules appear in Servekit's `go.mod` because Go has no separate
development-dependency section and the repository builds its examples in CI.
They are not imported by Servekit's root package or linked into applications
that import only Servekit.

## HTTP presentation

`WithOps(ops)` adds Opskit readiness to Servekit's built-in `GET /readyz`
decision. Readiness is evaluated in this order:

1. Servekit lifecycle readiness.
2. Opskit registry readiness.
3. Lightweight predicates supplied with `WithReadinessChecks(...)`.

The lifecycle gate remains first so startup, explicit readiness, drain delay,
and shutdown remain under Servekit's control.

Adding `WithOpsAdmin()` exposes two generic, read-only component routes:

- `GET /admin/components` returns registry inventory from `Registry.Entries()`.
  It does not evaluate component state.
- `GET /admin/components/{name}` returns a component snapshot from
  `Registry.Snapshot(...)`, including its passive status, readiness, and safe
  inspection data when supported.

Servekit does not run checks or dispatch commands through these routes. Active
operations belong in an execution layer that explicitly owns scheduling,
authorization, concurrency, retries, and other runtime policy.

## Keep probe work passive

Opskit component status, readiness, and inspection methods should return local
or cached state. A Kubernetes or load-balancer readiness probe must not fan out
into fresh network checks on every request.

Run expensive dependency checks in the background, cache their latest result
in the component that owns that state, and let Opskit aggregate the cached
readiness view. `WithOpsTimeout(...)` supplies a context deadline to Opskit
readiness and snapshot evaluation, but component implementations must still
observe context cancellation.

`WithReadinessChecks(...)` remains useful for a small standalone service that
does not need an Opskit registry. Treat those functions as fast readiness
predicates over local state, not as an active dependency-check scheduler.

## Protect operational detail

Admin routes are disabled unless `WithOpsAdmin()` is supplied. Once enabled,
they are unauthenticated unless the service adds
`WithOpsAdminAuthGate(...)` or equivalent network-level protection.

Readiness reasons, component identity, status attributes, and inspection data
may be serialized directly to HTTP responses. Components must therefore return
only information safe for the intended operational audience. Do not include
credentials, tokens, connection strings, user data, or other secrets.

See [`examples/operations`](../examples/operations) for the small Servekit and
Opskit path. Continue with
[`examples/kit-series-composition`](../examples/kit-series-composition) for the
canonical current-kit service shell.

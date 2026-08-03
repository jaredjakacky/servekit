# Kit Series Composition Example

This is the canonical single-service composition example for the currently
available Kit Series packages:

- Configkit owns typed configuration state.
- Dependkit owns cached dependency state and readiness.
- Workerkit owns periodic check execution and worker lifecycle.
- Opskit owns the shared operational contracts and registry.
- Servekit owns HTTP lifecycle, readiness, authentication, and presentation.
- The application explicitly assembles those pieces in `main.go`.

## Run

From the Servekit repository root:

```bash
go run ./examples/kit-series-composition
```

Then try:

```bash
curl -i http://localhost:8080/message
curl -i http://localhost:8080/readyz
curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components
curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components/config
curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components/dependencies
curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components/workers
```

`/readyz` and `/admin/components` read cached state. They do not run dependency
checks or dispatch commands. Workerkit-specific command and lifecycle HTTP
controls are intentionally outside this flagship; see Workerkit's focused
`opshttp` examples when a service genuinely needs those active routes.

The hard-coded development token is only for this local example. Production
services need deployment-appropriate authentication and authorization.

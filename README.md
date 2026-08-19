# Servekit

[![Release](https://img.shields.io/github/v/release/jaredjakacky/servekit?sort=semver)](https://github.com/jaredjakacky/servekit/releases)
[![CI](https://github.com/jaredjakacky/servekit/actions/workflows/ci.yaml/badge.svg)](https://github.com/jaredjakacky/servekit/actions/workflows/ci.yaml)
[![Go Support](https://img.shields.io/badge/go%20support-1.26.x%20%7C%201.26.x-00ADD8)](https://github.com/jaredjakacky/servekit/actions/workflows/ci.yaml)
[![License](https://img.shields.io/github/license/jaredjakacky/servekit)](https://github.com/jaredjakacky/servekit/blob/main/LICENSE)

## Overview

Servekit is a small, `net/http`-first Go package for bootstrapping APIs and
services with a production-oriented baseline: probes, JSON response handling,
request and correlation IDs, access logs, panic recovery, graceful shutdown,
and OpenTelemetry tracing and metrics.

It is not a web framework. Applications keep using `http.Request`,
`http.Handler`, and `http.ServeMux`, and they continue to own domain behavior,
dependency injection, background work, configuration, and observability
backends. Servekit owns the reusable HTTP bootstrap around them.

## Installation

```bash
go get github.com/jaredjakacky/servekit
```

```go
import servekit "github.com/jaredjakacky/servekit"
```

The minimum supported Go version is declared in [`go.mod`](go.mod), and the
currently verified versions are listed in the
[CI workflow](.github/workflows/ci.yaml).

## Production Quick Start

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

`Run()` listens on `:8080` by default, marks the server ready once it begins
serving, and handles cancellation, `SIGINT`, `SIGTERM`, and graceful shutdown.
The application route returns JSON, and the server also exposes `GET /livez`,
`GET /readyz`, and `GET /version`.

## Defaults

| Area | Default behavior |
| --- | --- |
| HTTP model | Standard `http.Request`, `http.Handler`, and `http.ServeMux` |
| Structured routes | `Handle` encodes successful values and returned errors as JSON |
| Operational endpoints | `GET /livez`, `GET /readyz`, and `GET /version` |
| Lifecycle | Readiness transitions, signal handling, graceful shutdown, and forced closure after shutdown failure or timeout |
| Request context | Validated or generated request and correlation IDs |
| Observability | Structured access logs, panic recovery, request spans and metrics, and `Run()`-path connection metrics |
| OpenTelemetry ownership | Global providers and propagators unless the application supplies overrides |
| Server limits | `5s` read, `2s` read-header, `10s` write, `60s` idle, `15s` shutdown, `1 MiB` headers, and `4 MiB` request bodies |
| CORS | Disabled until configured explicitly |

These defaults are intentionally conservative, not universal. See the
[Usage Guide](docs/usage.md) for the complete server model, the
[Lifecycle Guide](docs/lifecycle.md) for readiness and shutdown behavior, and
the [Observability Guide](docs/observability.md) for logging, recovery, and
telemetry details.

## `Handle` or `HandleHTTP`

Use `Handle` for ordinary request/response endpoints that return one value or
one error:

```go
s.Handle(http.MethodGet, "/users/me", func(r *http.Request) (any, error) {
	return currentUser(r.Context())
})
```

Use `HandleHTTP` when an endpoint needs direct `net/http` response control:

```go
s.HandleHTTP(http.MethodGet, "/events", eventStreamHandler)
```

Typical raw-handler cases include streaming, reverse proxying, protocol
upgrades, and existing `http.Handler` implementations. Servekit preserves
runtime writer capabilities such as `http.Flusher` and `http.Hijacker` when
the underlying writer supports them.

## Kit Series Composition

When a service is composed from several Kit Series packages, use one Opskit
registry as the shared operational read model and let Servekit present it:

```go
ops := opskit.NewRegistry()

// The application registers Opskit components produced by sibling kits or by
// application code. Servekit does not depend on those packages directly.
ops.MustRegister(component, opskit.Required())

s := servekit.New(
	servekit.WithOps(
		ops,
		servekit.WithOpsAdmin(),
		servekit.WithOpsAdminAuthGate(requireAdmin),
	),
)
```

`GET /readyz` combines Servekit lifecycle readiness with Opskit registry
readiness. The opt-in `GET /admin/components` and
`GET /admin/components/{name}` routes present passive inventory and snapshots.
Protect admin routes with an application auth gate or equivalent network
policy. Servekit does not run checks or dispatch commands through those routes,
and it does not import sibling domain kits.

See [Kit Series Composition](docs/composition.md) for the ownership model,
[`examples/operations`](examples/operations) for the smallest Servekit and
Opskit path, and
[`examples/kit-series-composition`](examples/kit-series-composition) for the
canonical composition of the currently available kits.

## Learn More

- [Getting Started](docs/getting-started.md): run a first service and try its routes
- [Usage Guide](docs/usage.md): understand the normal route path and server defaults
- [Kit Series Composition](docs/composition.md): compose Opskit-backed readiness and admin presentation
- [Lifecycle and Probes](docs/lifecycle.md): configure readiness, health, and shutdown
- [Observability and Middleware](docs/observability.md): customize IDs, logs, recovery, OpenTelemetry, and CORS
- [Advanced Guide](docs/advanced.md): integrate custom encoders, an existing mux, or an external server
- [API Map](docs/api.md): browse the complete exported surface by responsibility

Start with [`examples/basic`](examples/basic), then use
[`examples/operations`](examples/operations) for Opskit composition,
[`examples/endpoint-controls`](examples/endpoint-controls) for route policy, or
[`examples/telemetry`](examples/telemetry) for an application-owned telemetry
SDK. The [Examples Guide](docs/examples.md) and
[examples index](examples/README.md) cover the complete runnable set.

## Maintenance

Servekit is a small open source library maintained on a best-effort basis.

The active development line lives on `main`, and that is the only line actively maintained unless explicitly noted otherwise. The minimum supported Go version is declared in [`go.mod`](go.mod), and the Go versions currently verified in CI are listed in [`.github/workflows/ci.yaml`](.github/workflows/ci.yaml).

Compatibility-impacting changes should be called out explicitly in release notes or release descriptions. Long-lived maintenance branches and backports are not planned unless explicitly noted.

## Development

This repository uses `make` for local verification:

```bash
make verify
make build-examples
make test-race
make govulncheck
```

`make verify` checks formatting, runs `go vet`, runs tests, builds the runnable
examples, and verifies that every checked-in Go module is tidy.
`make build-examples` is available when you only want to compile the runnable
examples.

CI runs verification and race tests on the supported Go versions.

## Releasing

Releases start from the repository's **Actions → Release → Run workflow**
screen. Select `main` and enter the new semantic version, such as `v0.5.0`.
The workflow validates that version against the module path and runs
`make verify`, `make test-race`, and `make govulncheck` against the exact
selected commit on every supported Go version. Only after all checks pass does
it create the version tag and GitHub Release.

Do not create or push `v*` tags manually; doing so would publish a Go module
version without the release workflow's pre-publication checks.

## Issues and Scope

Bug reports, documentation fixes, small API ergonomics improvements, and compatibility issues are welcome.

Servekit is intentionally scoped as a small `net/http`-first service bootstrap library. Large framework features are likely out of scope, including custom router DSLs, dependency injection, config loading, background job systems, service discovery, and observability backend management.

For security issues, please follow [`SECURITY.md`](SECURITY.md) instead of opening a public issue.

## License

[MIT](LICENSE)

# Servekit Examples

This page is the directory index for Servekit's runnable examples.

These examples are part of the public documentation, not just smoke-test programs.

Use this page when you want the short version: what examples exist, what each one demonstrates, and what to run next.

If you want the fuller narrative walkthrough, start with the [Examples Guide](../docs/examples.md).

Read the examples as a progression from the normal Servekit path through the shared Opskit composition model, then outward into telemetry, route-level controls, customization, and advanced integration.

## Read Order

1. [basic](basic)
2. [operations](operations)
3. [kit-series-composition](kit-series-composition)
4. [telemetry](telemetry)
5. [endpoint-controls](endpoint-controls)
6. [custom-encoding](custom-encoding)
7. [readiness](readiness)
8. [logging](logging)
9. [cors](cors)
10. [external-server](external-server)
11. [advanced-composition](advanced-composition)
12. [streaming](streaming)
13. [reverse-proxy](reverse-proxy)
14. [response-capture](response-capture)

## What Each Example Shows

- [basic](basic)
  The core off-the-shelf story: one small business route plus `New`, `Handle`, `Run`, built-in probes, JSON encoding, IDs, access logs, panic recovery, and OpenTelemetry middleware that uses application-owned global providers.
- [operations](operations)
  The small Kit Series presentation story: one shared Opskit registry, required and informational components, Opskit-backed readiness, authenticated component inventory, and passive component snapshots without any domain-kit-specific Servekit integration.
- [kit-series-composition](kit-series-composition)
  The canonical current-kit service shell: typed Configkit state, cached Dependkit readiness, Workerkit-owned check execution and lifecycle, one shared Opskit registry, and passive Servekit HTTP presentation. Focused Workerkit examples cover active HTTP controls separately.
- [telemetry](telemetry)
  The focused OpenTelemetry story: example-owned stdout exporters, global tracer and meter providers, request spans, request metrics, and `Run(...)`-path connection metrics without any Servekit-specific telemetry options.
- [endpoint-controls](endpoint-controls)
  The focused middleware and route-level policy story: `WithMiddleware(...)`, `WithAuthCheck(...)`, `WithAuthGate(...)`, `WithEndpointMiddleware(...)`, `WithBodyLimit(...)`, `WithEndpointTimeout(...)`, and route-local observability suppression.
- [custom-encoding](custom-encoding)
  Global and per-endpoint response contract customization with `WithResponseEncoder(...)`, `WithErrorEncoder(...)`, and `WithEndpointResponseEncoder(...)`.
- [readiness](readiness)
  The standalone path for a small service without Opskit: a local readiness predicate, custom `/healthz`, warmup sequencing, and shutdown drain delay.
- [logging](logging)
  Custom `slog` setup, `http.Server.ErrorLog` wiring, request IDs, and panic recovery behavior.
- [cors](cors)
  Opt-in browser CORS policy with preflight handling and credentialed origin allowlists.
- [external-server](external-server)
  Advanced integration with an existing mux, `Handler()`, and an externally owned `http.Server`.
- [advanced-composition](advanced-composition)
  A late-stage reference configuration that composes custom encoders, readiness, health, CORS, logging, telemetry overrides, endpoint overrides, and one raw handler in a single runnable service.
- [streaming](streaming)
  The dedicated `http.Flusher` example: raw SSE-style streaming through `HandleHTTP(...)`.
- [reverse-proxy](reverse-proxy)
  Reverse proxy integration through the raw handler path.
- [response-capture](response-capture)
  The lower-level engineering story, including the concrete `http.Hijacker` route: implicit status capture, response byte counting, and preserved `Flush`/`Hijack` behavior.

## Why This Structure Exists

The examples are intentionally organized to answer five reader questions:

- "What is the shortest useful Servekit service?"
- "How does Servekit present a composed service without depending on domain kits?"
- "What observability do I get by default?"
- "How do I change one route without changing the whole server?"
- "Does the raw HTTP escape hatch still behave credibly for advanced use cases?"

That is why the examples move from the vanilla path outward instead of being presented as an unordered list of demos.

## Run Them

Run examples from the repository root:

```bash
go run ./examples/<name>

# for example
go run ./examples/basic
go run ./examples/operations
go run ./examples/kit-series-composition
go run ./examples/telemetry
go run ./examples/endpoint-controls
go run ./examples/advanced-composition
```

Each example prints suggested `curl` commands on startup. The source comments in each `main.go` explain the purpose of the example and the behavior worth noticing.

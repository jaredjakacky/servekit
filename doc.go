// Package servekit provides a small, net/http-first bootstrap layer for HTTP services.
//
// The package focuses on production defaults for server lifecycle, middleware
// wiring, health/readiness probes, request and correlation IDs, JSON encoding,
// panic logging, graceful shutdown, and opt-in CORS.
//
// When OpenTelemetry is enabled, Servekit also wires request tracing and
// request metrics into the default handler stack. On the built-in Run path it
// additionally wires server-level connection metrics, because those depend on
// http.Server.ConnState rather than only on request middleware.
//
// Servekit is intentionally not a web framework: it does not define routing
// syntax beyond http.ServeMux patterns, does not impose dependency injection,
// and does not hide net/http primitives.
//
// Typical usage is to construct a Server with New, register handlers with
// Handle or HandleHTTP, then call Run from your main package.
package servekit

module github.com/jaredjakacky/servekit/examples/kit-series-composition

go 1.25.0

require (
	github.com/jaredjakacky/configkit v0.3.0
	github.com/jaredjakacky/dependkit v0.3.0
	github.com/jaredjakacky/opskit v0.3.0
	github.com/jaredjakacky/servekit v0.4.0
	github.com/jaredjakacky/workerkit v0.5.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
)

// Build this integration example against the Servekit checkout without making
// the sibling kits part of Servekit's published module graph.
replace github.com/jaredjakacky/servekit => ../..

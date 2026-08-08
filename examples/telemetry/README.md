# Telemetry Example

This example installs application-owned OpenTelemetry SDK providers and stdout
exporters so Servekit's default tracing, request metrics, and Run-path
connection metrics are directly visible.

The example uses a nested Go module to keep SDK and exporter dependencies out
of Servekit's published root module graph. Its local `replace` directive builds
against the checked-out Servekit source.

## Run

From the Servekit repository root:

```bash
go -C examples/telemetry run .
```

The program prints suggested requests and the telemetry to watch for.

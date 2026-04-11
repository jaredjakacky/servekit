package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// installTelemetry configures process-wide stdout trace and metric exporters.
//
// This helper exists only to make Servekit's default OTel behavior visible
// when the example runs locally. The Servekit server itself is still created
// with the normal defaults and no telemetry-specific Servekit options.
func installTelemetry() (func(context.Context) error, error) {
	traceExporter, err := stdouttrace.New(
		stdouttrace.WithWriter(os.Stdout),
		stdouttrace.WithPrettyPrint(),
	)
	if err != nil {
		return nil, fmt.Errorf("create stdout trace exporter: %w", err)
	}

	metricExporter, err := stdoutmetric.New(
		stdoutmetric.WithWriter(os.Stdout),
		stdoutmetric.WithPrettyPrint(),
	)
	if err != nil {
		return nil, fmt.Errorf("create stdout metric exporter: %w", err)
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("servekit-telemetry-example"),
		attribute.String("example", "telemetry"),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(traceExporter),
		sdktrace.WithResource(res),
	)

	reader := metric.NewPeriodicReader(
		metricExporter,
		metric.WithInterval(2*time.Second),
		metric.WithTimeout(2*time.Second),
	)
	mp := metric.NewMeterProvider(
		metric.WithReader(reader),
		metric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func(ctx context.Context) error {
		return errors.Join(mp.Shutdown(ctx), tp.Shutdown(ctx))
	}, nil
}

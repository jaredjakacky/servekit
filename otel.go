package servekit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/jaredjakacky/servekit"

type observabilityConfig struct {
	tracerProvider   trace.TracerProvider
	meterProvider    metric.MeterProvider
	propagator       propagation.TextMapPropagator
	attributes       func(*http.Request) []attribute.KeyValue
	spanName         func(*http.Request, string) string
	routeLabel       func(*http.Request) string
	skipTelemetry    func(*http.Request) bool
	enablePanicCount bool
	serverMetrics    *otelServerMetrics
	// panicCountSet distinguishes "explicitly false" from the bool zero value so
	// default resolution can preserve panic counting unless the user opts out.
	panicCountSet bool
}

func defaultObservabilityConfig() observabilityConfig {
	return observabilityConfig{
		attributes:       func(*http.Request) []attribute.KeyValue { return nil },
		spanName:         defaultSpanName,
		routeLabel:       defaultRouteLabel,
		enablePanicCount: true,
	}
}

func (s *Server) observabilityMiddlewares() []Middleware {
	obs := resolvedObservabilityConfig(s.observabilityOverrides)
	obs.serverMetrics = s.serverMetricsCollector()
	obs.skipTelemetry = s.routeSkipsTelemetry
	tracing := otelTracingMiddleware(obs)
	metrics := newOTelMetricsMiddleware(obs)
	return []Middleware{tracing, metrics}
}

// resolvedObservabilityConfig merges explicit user overrides with Servekit's
// OTel defaults and global providers into a middleware-ready config snapshot.
func resolvedObservabilityConfig(overrides observabilityConfig) observabilityConfig {
	obs := defaultObservabilityConfig()
	obs.tracerProvider = overrides.tracerProvider
	if obs.tracerProvider == nil {
		obs.tracerProvider = otel.GetTracerProvider()
	}
	obs.meterProvider = overrides.meterProvider
	if obs.meterProvider == nil {
		obs.meterProvider = otel.GetMeterProvider()
	}
	obs.propagator = overrides.propagator
	if obs.propagator == nil {
		obs.propagator = otel.GetTextMapPropagator()
	}
	if overrides.attributes != nil {
		obs.attributes = overrides.attributes
	}
	if overrides.spanName != nil {
		obs.spanName = overrides.spanName
	}
	if overrides.routeLabel != nil {
		obs.routeLabel = overrides.routeLabel
	}
	if overrides.panicCountSet {
		obs.enablePanicCount = overrides.enablePanicCount
		obs.panicCountSet = true
	}
	return obs
}

// WithOpenTelemetryEnabled toggles Servekit's built-in OTel middleware.
func WithOpenTelemetryEnabled(enabled bool) Option {
	return func(s *Server) { s.enableOpenTelemetry = enabled }
}

// WithTracerProvider sets the tracer provider used by OTel middleware.
//
// When nil, Servekit uses otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(s *Server) { s.observabilityOverrides.tracerProvider = tp }
}

// WithMeterProvider sets the meter provider used by OTel middleware.
//
// When nil, Servekit uses otel.GetMeterProvider().
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(s *Server) { s.observabilityOverrides.meterProvider = mp }
}

// WithPropagator sets the text map propagator used to extract incoming context.
//
// When nil, Servekit uses otel.GetTextMapPropagator().
func WithPropagator(p propagation.TextMapPropagator) Option {
	return func(s *Server) { s.observabilityOverrides.propagator = p }
}

// WithOTelAttributes appends request attributes to spans and metrics.
func WithOTelAttributes(fn func(*http.Request) []attribute.KeyValue) Option {
	return func(s *Server) {
		if fn != nil {
			s.observabilityOverrides.attributes = fn
		}
	}
}

// WithSpanNameFormatter overrides per-request span naming.
func WithSpanNameFormatter(fn func(*http.Request, string) string) Option {
	return func(s *Server) {
		if fn != nil {
			s.observabilityOverrides.spanName = fn
		}
	}
}

// WithRouteLabeler overrides the low-cardinality route label strategy.
func WithRouteLabeler(fn func(*http.Request) string) Option {
	return func(s *Server) {
		if fn != nil {
			s.observabilityOverrides.routeLabel = fn
		}
	}
}

// WithOTelPanicMetricEnabled enables or disables panic counter metrics.
func WithOTelPanicMetricEnabled(enabled bool) Option {
	return func(s *Server) {
		s.observabilityOverrides.enablePanicCount = enabled
		s.observabilityOverrides.panicCountSet = true
	}
}

func otelTracingMiddleware(obs observabilityConfig) Middleware {
	tracer := obs.tracerProvider.Tracer(instrumentationName)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Continue any incoming trace context and start the server span for this request.
			extractedCtx := obs.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			r = r.WithContext(extractedCtx)
			if !skipTelemetryRequested(r) && obs.skipTelemetry != nil && obs.skipTelemetry(r) {
				r = markSkipTelemetry(r)
			}
			if skipTelemetryRequested(r) {
				next.ServeHTTP(w, r)
				return
			}
			r = withMatchedRoute(r)
			initialRoute := obs.routeLabel(r)
			spanName := obs.spanName(r, initialRoute)
			ctx, span := tracer.Start(r.Context(), spanName, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()
			r = r.WithContext(ctx)
			rw := captureWriter(w, responseCaptureHooks{
				trackHijack: func(conn net.Conn) net.Conn {
					if obs.serverMetrics == nil {
						return conn
					}
					return obs.serverMetrics.trackHijackedConnection(conn)
				},
			})

			// Attach stable request metadata early so the span is useful even on failures.
			attrs := spanAttributes(r, initialRoute, obs.attributes)
			span.SetAttributes(attrs...)

			defer func() {
				// Finalize the span from the observed request outcome.
				finalRoute := obs.routeLabel(r)
				if finalRoute != initialRoute {
					span.SetName(obs.spanName(r, finalRoute))
					if finalRoute != "" {
						span.SetAttributes(semconv.HTTPRoute(finalRoute))
					}
				}
				rec := recover()
				status := completedStatusCode(rw, rec != nil)
				span.SetAttributes(semconv.HTTPResponseStatusCode(status))
				if rec != nil {
					span.RecordError(fmt.Errorf("panic: %v", rec))
					span.SetStatus(codes.Error, "panic")
					panic(rec)
				}
				if status >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, http.StatusText(status))
				}
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

type otelMetrics struct {
	requestCount      metric.Int64Counter
	duration          metric.Float64Histogram
	inFlight          metric.Int64UpDownCounter
	panicCount        metric.Int64Counter
	timeoutCount      metric.Int64Counter
	cancellationCount metric.Int64Counter
	authRejectCount   metric.Int64Counter
	enablePanics      bool
	customAttrs       func(*http.Request) []attribute.KeyValue
	routeExtractor    func(*http.Request) string
	skipTelemetry     func(*http.Request) bool
}

type otelServerMetrics struct {
	activeConnections         metric.Int64UpDownCounter
	activeHijackedConnections metric.Int64UpDownCounter
	managedConnections        sync.Map
	hijackedConnections       sync.Map
}

func newOTelServerMetrics(obs observabilityConfig) *otelServerMetrics {
	meter := obs.meterProvider.Meter(instrumentationName)
	activeConnections, _ := meter.Int64UpDownCounter("http.server.connection.active", metric.WithUnit("{connection}"))
	activeHijackedConnections, _ := meter.Int64UpDownCounter("http.server.connection.hijacked.active", metric.WithUnit("{connection}"))
	return &otelServerMetrics{
		activeConnections:         activeConnections,
		activeHijackedConnections: activeHijackedConnections,
	}
}

func (m *otelServerMetrics) connState(conn net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		if _, loaded := m.managedConnections.LoadOrStore(conn, struct{}{}); !loaded {
			m.activeConnections.Add(context.Background(), 1)
		}
	case http.StateHijacked:
		if _, loaded := m.managedConnections.LoadAndDelete(conn); loaded {
			m.activeConnections.Add(context.Background(), -1)
		}
	case http.StateClosed:
		if _, loaded := m.managedConnections.LoadAndDelete(conn); loaded {
			m.activeConnections.Add(context.Background(), -1)
		}
	}
}

func (m *otelServerMetrics) trackHijackedConnection(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	if _, loaded := m.hijackedConnections.LoadOrStore(conn, struct{}{}); loaded {
		return conn
	}
	m.activeHijackedConnections.Add(context.Background(), 1)
	return &trackedHijackedConn{Conn: conn, metrics: m}
}

type trackedHijackedConn struct {
	net.Conn
	metrics *otelServerMetrics
	closed  sync.Once
}

func (c *trackedHijackedConn) Close() error {
	err := c.Conn.Close()
	c.closed.Do(func() {
		if _, loaded := c.metrics.hijackedConnections.LoadAndDelete(c.Conn); loaded {
			c.metrics.activeHijackedConnections.Add(context.Background(), -1)
		}
	})
	return err
}

// newOTelMetricsMiddleware records HTTP request metrics using semconv-aligned
// short-request buckets with a modest long-tail extension for slower handlers.
func newOTelMetricsMiddleware(obs observabilityConfig) Middleware {
	meter := obs.meterProvider.Meter(instrumentationName)

	requestCount, _ := meter.Int64Counter("http.server.request.count", metric.WithUnit("{request}"))
	duration, _ := meter.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1.0,
			2.5, 5.0, 7.5, 10.0, 15.0, 30.0, 60.0,
		),
	)
	inFlight, _ := meter.Int64UpDownCounter("http.server.request.in_flight", metric.WithUnit("{request}"))
	panicCount, _ := meter.Int64Counter("http.server.request.panic.count", metric.WithUnit("{panic}"))
	timeoutCount, _ := meter.Int64Counter("http.server.request.timeout.count", metric.WithUnit("{timeout}"))
	cancellationCount, _ := meter.Int64Counter("http.server.request.cancellation.count", metric.WithUnit("{request}"))
	authRejectCount, _ := meter.Int64Counter("http.server.request.auth_rejection.count", metric.WithUnit("{request}"))

	collector := otelMetrics{
		requestCount:      requestCount,
		duration:          duration,
		inFlight:          inFlight,
		panicCount:        panicCount,
		timeoutCount:      timeoutCount,
		cancellationCount: cancellationCount,
		authRejectCount:   authRejectCount,
		enablePanics:      obs.enablePanicCount,
		customAttrs:       obs.attributes,
		routeExtractor:    obs.routeLabel,
		skipTelemetry:     obs.skipTelemetry,
	}

	return collector.middleware()
}

func (m otelMetrics) middleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !skipTelemetryRequested(r) && m.skipTelemetry != nil && m.skipTelemetry(r) {
				r = markSkipTelemetry(r)
			}
			if skipTelemetryRequested(r) {
				next.ServeHTTP(w, r)
				return
			}
			r = withMatchedRoute(r)
			r = withRequestOutcome(r)
			start := time.Now()
			route := m.routeExtractor(r)
			rw := captureWriter(w, responseCaptureHooks{})
			base := metricAttributes(r, route, m.customAttrs)
			m.inFlight.Add(r.Context(), 1, metric.WithAttributes(base...))
			defer m.inFlight.Add(r.Context(), -1, metric.WithAttributes(base...))
			defer func() {
				rec := recover()
				status := completedStatusCode(rw, rec != nil)
				finalRoute := m.routeExtractor(r)
				attrs := metricAttributes(r, finalRoute, m.customAttrs)
				attrs = append(attrs, semconv.HTTPResponseStatusCode(status))
				if rec != nil && m.enablePanics {
					m.panicCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
				}
				if outcome := requestOutcomeState(r); outcome != nil {
					if outcome.timedOut {
						m.timeoutCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
					}
					if outcome.canceled {
						m.cancellationCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
					}
					if outcome.authRejected {
						m.authRejectCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
					}
				}
				m.requestCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
				m.duration.Record(r.Context(), time.Since(start).Seconds(), metric.WithAttributes(attrs...))
				if rec != nil {
					panic(rec)
				}
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

func spanAttributes(r *http.Request, route string, extra func(*http.Request) []attribute.KeyValue) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(r.Method),
		semconv.UserAgentOriginal(r.UserAgent()),
	}
	if scheme := requestScheme(r); scheme != "" {
		attrs = append(attrs, semconv.URLScheme(scheme))
	}
	if route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	if extra != nil {
		attrs = append(attrs, extra(r)...)
	}
	return attrs
}

func metricAttributes(r *http.Request, route string, extra func(*http.Request) []attribute.KeyValue) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(r.Method),
	}
	if scheme := requestScheme(r); scheme != "" {
		attrs = append(attrs, semconv.URLScheme(scheme))
	}
	if route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	if extra != nil {
		attrs = append(attrs, extra(r)...)
	}
	return attrs
}

func requestScheme(r *http.Request) string {
	if r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return ""
}

func defaultRouteLabel(r *http.Request) string {
	if route := matchedRoutePath(r); route != "" {
		return route
	}
	if r.Pattern == "" {
		return ""
	}
	if _, path, ok := strings.Cut(r.Pattern, " "); ok {
		return path
	}
	return r.Pattern
}

func defaultSpanName(r *http.Request, route string) string {
	if route != "" {
		return r.Method + " " + route
	}
	return r.Method
}

// TraceIDFromContext returns the active trace ID for the request context.
func TraceIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// SpanIDFromContext returns the active span ID for the request context.
func SpanIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}

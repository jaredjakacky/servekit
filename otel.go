package servekit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/jaredjakacky/servekit"

const (
	panicMetricName              = "servekit.http.server.request.panic.count"
	timeoutMetricName            = "servekit.http.server.request.timeout.count"
	cancellationMetricName       = "servekit.http.server.request.cancellation.count"
	authRejectionMetricName      = "servekit.http.server.request.auth_rejection.count"
	activeConnectionMetricName   = "servekit.http.server.connection.active"
	hijackedConnectionMetricName = "servekit.http.server.connection.hijacked.active"
)

type observabilityConfig struct {
	tracerProvider   trace.TracerProvider
	meterProvider    metric.MeterProvider
	propagator       propagation.TextMapPropagator
	spanAttributes   func(*http.Request) []attribute.KeyValue
	metricAttributes func(*http.Request) []attribute.KeyValue
	spanName         func(*http.Request, string) string
	routeLabel       func(*http.Request) string
	skipTelemetry    func(*http.Request) bool
	enablePanicCount bool
	serverMetrics    *otelServerMetrics
	knownMethods     map[string]httpconv.RequestMethodAttr
	// panicCountSet distinguishes "explicitly false" from the bool zero value so
	// default resolution can preserve panic counting unless the user opts out.
	panicCountSet bool
}

func defaultObservabilityConfig() observabilityConfig {
	return observabilityConfig{
		spanAttributes:   func(*http.Request) []attribute.KeyValue { return nil },
		metricAttributes: func(*http.Request) []attribute.KeyValue { return nil },
		routeLabel:       defaultRouteLabel,
		enablePanicCount: true,
		knownMethods:     knownHTTPMethods(),
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
	if overrides.spanAttributes != nil {
		obs.spanAttributes = overrides.spanAttributes
	}
	if overrides.metricAttributes != nil {
		obs.metricAttributes = overrides.metricAttributes
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

// WithOTelSpanAttributes appends request-derived attributes to server spans.
//
// Span attributes may contain high-cardinality values that would be unsafe on
// metrics, such as request identifiers or full URLs. Use
// WithOTelMetricAttributes separately for bounded metric dimensions.
func WithOTelSpanAttributes(fn func(*http.Request) []attribute.KeyValue) Option {
	return func(s *Server) {
		if fn != nil {
			s.observabilityOverrides.spanAttributes = fn
		}
	}
}

// WithOTelMetricAttributes appends low-cardinality request attributes to
// built-in request metrics.
//
// Values returned by fn must come from a bounded set. Request IDs, tenant IDs,
// raw URLs, and other unbounded values must not be used as metric attributes.
func WithOTelMetricAttributes(fn func(*http.Request) []attribute.KeyValue) Option {
	return func(s *Server) {
		if fn != nil {
			s.observabilityOverrides.metricAttributes = fn
		}
	}
}

// WithSpanNameFormatter overrides per-request span naming.
//
// Returned names should be bounded operation names or route templates, not raw
// request paths or arbitrary request methods.
func WithSpanNameFormatter(fn func(*http.Request, string) string) Option {
	return func(s *Server) {
		if fn != nil {
			s.observabilityOverrides.spanName = fn
		}
	}
}

// WithRouteLabeler overrides the low-cardinality route label strategy used by
// spans and request metrics.
//
// Returned labels must be bounded route templates, not raw request paths.
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
			start := time.Now()
			timing := &otelRequestTiming{start: start}
			r = r.WithContext(context.WithValue(r.Context(), otelRequestTimingKey{}, timing))
			ctx, span := tracer.Start(
				r.Context(),
				formatSpanName(obs, r, ""),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithTimestamp(start),
				trace.WithAttributes(spanAttributes(r, obs.knownMethods)...),
			)
			r = r.WithContext(ctx)
			rw := captureWriter(w, responseCaptureHooks{
				trackHijack: func(conn net.Conn) net.Conn {
					if obs.serverMetrics == nil {
						return conn
					}
					return obs.serverMetrics.trackHijackedConnection(conn)
				},
			})

			defer func() {
				rec := recover()
				end := timing.end
				if end.IsZero() {
					end = time.Now()
				}
				defer span.End(trace.WithTimestamp(end))
				// Finalize the span from the observed request outcome.
				finalRoute := obs.routeLabel(r)
				span.SetName(formatSpanName(obs, r, finalRoute))
				if finalRoute != "" {
					span.SetAttributes(semconv.HTTPRoute(finalRoute))
				}
				status, statusKnown := observedStatusCode(rw, rec)
				if statusKnown {
					span.SetAttributes(semconv.HTTPResponseStatusCode(status))
				}
				if errorType := responseErrorType(status, statusKnown, rec); errorType != "" {
					span.SetAttributes(semconv.ErrorTypeKey.String(errorType))
				}
				if rec != nil {
					span.RecordError(fmt.Errorf("panic: %v", rec))
					span.SetStatus(codes.Error, "panic")
				} else if statusKnown && status >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, "")
				}
				if rec != nil {
					panic(rec)
				}
			}()

			span.SetAttributes(customAttributes(r, obs.spanAttributes)...)
			next.ServeHTTP(rw, r)
		})
	}
}

type otelMetrics struct {
	duration          httpconv.ServerRequestDuration
	activeRequests    httpconv.ServerActiveRequests
	panicCount        metric.Int64Counter
	timeoutCount      metric.Int64Counter
	cancellationCount metric.Int64Counter
	authRejectCount   metric.Int64Counter
	enablePanics      bool
	customAttrs       func(*http.Request) []attribute.KeyValue
	routeExtractor    func(*http.Request) string
	skipTelemetry     func(*http.Request) bool
	knownMethods      map[string]httpconv.RequestMethodAttr
}

type otelServerMetrics struct {
	activeConnections         metric.Int64UpDownCounter
	activeHijackedConnections metric.Int64UpDownCounter
	managedConnections        sync.Map
	hijackedConnections       sync.Map
}

func newOTelServerMetrics(obs observabilityConfig) *otelServerMetrics {
	meter := obs.meterProvider.Meter(instrumentationName)
	activeConnections, _ := meter.Int64UpDownCounter(
		activeConnectionMetricName,
		metric.WithDescription("Number of active connections managed by Servekit's net/http server."),
		metric.WithUnit("{connection}"),
	)
	activeHijackedConnections, _ := meter.Int64UpDownCounter(
		hijackedConnectionMetricName,
		metric.WithDescription("Number of active hijacked connections tracked by Servekit."),
		metric.WithUnit("{connection}"),
	)
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

// newOTelMetricsMiddleware records standard HTTP request duration and active
// request instruments alongside explicitly namespaced Servekit outcome metrics.
func newOTelMetricsMiddleware(obs observabilityConfig) Middleware {
	meter := obs.meterProvider.Meter(instrumentationName)

	duration, _ := httpconv.NewServerRequestDuration(meter)
	activeRequests, _ := httpconv.NewServerActiveRequests(meter)
	panicCount, _ := meter.Int64Counter(
		panicMetricName,
		metric.WithDescription("Number of HTTP server requests that ended in a panic."),
		metric.WithUnit("{request}"),
	)
	timeoutCount, _ := meter.Int64Counter(
		timeoutMetricName,
		metric.WithDescription("Number of HTTP server requests that exceeded a Servekit endpoint timeout."),
		metric.WithUnit("{request}"),
	)
	cancellationCount, _ := meter.Int64Counter(
		cancellationMetricName,
		metric.WithDescription("Number of HTTP server requests canceled by the client."),
		metric.WithUnit("{request}"),
	)
	authRejectCount, _ := meter.Int64Counter(
		authRejectionMetricName,
		metric.WithDescription("Number of HTTP server requests rejected by Servekit endpoint authentication."),
		metric.WithUnit("{request}"),
	)

	collector := otelMetrics{
		duration:          duration,
		activeRequests:    activeRequests,
		panicCount:        panicCount,
		timeoutCount:      timeoutCount,
		cancellationCount: cancellationCount,
		authRejectCount:   authRejectCount,
		enablePanics:      obs.enablePanicCount,
		customAttrs:       obs.metricAttributes,
		routeExtractor:    obs.routeLabel,
		skipTelemetry:     obs.skipTelemetry,
		knownMethods:      obs.knownMethods,
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
			timing := otelTimingFromContext(r.Context())
			if timing == nil {
				timing = &otelRequestTiming{start: time.Now()}
			}
			rw := captureWriter(w, responseCaptureHooks{})
			method := normalizedHTTPMethod(r.Method, m.knownMethods)
			scheme := requestScheme(r)
			activeAttrs := customAttributes(r, m.customAttrs)
			m.activeRequests.Add(r.Context(), 1, method, scheme, activeAttrs...)
			defer m.activeRequests.Add(r.Context(), -1, method, scheme, activeAttrs...)
			defer func() {
				rec := recover()
				end := time.Now()
				timing.end = end
				status, statusKnown := observedStatusCode(rw, rec)
				finalRoute := m.routeExtractor(r)
				custom := customAttributes(r, m.customAttrs)
				attrs := servekitMetricAttributes(r, finalRoute, custom, m.knownMethods)
				durationAttrs := standardDurationAttributes(r, finalRoute, custom)
				if statusKnown {
					attrs = append(attrs, semconv.HTTPResponseStatusCode(status))
					durationAttrs = append(durationAttrs, semconv.HTTPResponseStatusCode(status))
				}
				if errorType := responseErrorType(status, statusKnown, rec); errorType != "" {
					attrs = append(attrs, semconv.ErrorTypeKey.String(errorType))
					durationAttrs = append(durationAttrs, semconv.ErrorTypeKey.String(errorType))
				}
				if rec != nil && rec != http.ErrAbortHandler && m.enablePanics {
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
				m.duration.Record(r.Context(), end.Sub(timing.start).Seconds(), method, scheme, durationAttrs...)
				if rec != nil {
					panic(rec)
				}
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

func spanAttributes(r *http.Request, knownMethods map[string]httpconv.RequestMethodAttr) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	method := normalizedHTTPMethod(r.Method, knownMethods)
	attrs = append(attrs,
		semconv.HTTPRequestMethodKey.String(string(method)),
		semconv.URLScheme(requestScheme(r)),
	)
	if method == httpconv.RequestMethodOther && r.Method != "" {
		attrs = append(attrs, semconv.HTTPRequestMethodOriginal(r.Method))
	}
	if r.URL != nil {
		attrs = append(attrs, semconv.URLPath(r.URL.Path))
	}
	if userAgent := r.UserAgent(); userAgent != "" {
		attrs = append(attrs, semconv.UserAgentOriginal(userAgent))
	}
	if version := requestProtocolVersion(r); version != "" {
		attrs = append(attrs, semconv.NetworkProtocolVersion(version))
	}
	return attrs
}

func servekitMetricAttributes(r *http.Request, route string, custom []attribute.KeyValue, knownMethods map[string]httpconv.RequestMethodAttr) []attribute.KeyValue {
	attrs := append([]attribute.KeyValue(nil), custom...)
	attrs = append(attrs,
		semconv.HTTPRequestMethodKey.String(string(normalizedHTTPMethod(r.Method, knownMethods))),
		semconv.URLScheme(requestScheme(r)),
	)
	if version := requestProtocolVersion(r); version != "" {
		attrs = append(attrs, semconv.NetworkProtocolVersion(version))
	}
	if route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	return attrs
}

func standardDurationAttributes(r *http.Request, route string, custom []attribute.KeyValue) []attribute.KeyValue {
	attrs := append([]attribute.KeyValue(nil), custom...)
	if version := requestProtocolVersion(r); version != "" {
		attrs = append(attrs, semconv.NetworkProtocolVersion(version))
	}
	if route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	return attrs
}

func customAttributes(r *http.Request, fn func(*http.Request) []attribute.KeyValue) []attribute.KeyValue {
	if fn == nil {
		return nil
	}
	return append([]attribute.KeyValue(nil), fn(r)...)
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func requestProtocolVersion(r *http.Request) string {
	if r.ProtoMajor <= 0 {
		return ""
	}
	if r.ProtoMajor == 1 {
		return "1." + strconv.Itoa(r.ProtoMinor)
	}
	if r.ProtoMinor == 0 {
		return strconv.Itoa(r.ProtoMajor)
	}
	return strconv.Itoa(r.ProtoMajor) + "." + strconv.Itoa(r.ProtoMinor)
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

func formatSpanName(obs observabilityConfig, r *http.Request, route string) string {
	if obs.spanName != nil {
		return obs.spanName(r, route)
	}
	return defaultSpanName(r, route, obs.knownMethods)
}

func defaultSpanName(r *http.Request, route string, knownMethods map[string]httpconv.RequestMethodAttr) string {
	method := normalizedHTTPMethod(r.Method, knownMethods)
	name := string(method)
	if method == httpconv.RequestMethodOther {
		name = "HTTP"
	}
	if route != "" {
		return name + " " + route
	}
	return name
}

func normalizedHTTPMethod(method string, knownMethods map[string]httpconv.RequestMethodAttr) httpconv.RequestMethodAttr {
	if normalized, ok := knownMethods[method]; ok {
		return normalized
	}
	return httpconv.RequestMethodOther
}

func knownHTTPMethods() map[string]httpconv.RequestMethodAttr {
	if configured, ok := os.LookupEnv("OTEL_INSTRUMENTATION_HTTP_KNOWN_METHODS"); ok {
		methods := make(map[string]httpconv.RequestMethodAttr)
		for _, method := range strings.Split(configured, ",") {
			if method != "" {
				methods[method] = httpconv.RequestMethodAttr(method)
			}
		}
		return methods
	}
	return map[string]httpconv.RequestMethodAttr{
		http.MethodConnect: httpconv.RequestMethodConnect,
		http.MethodDelete:  httpconv.RequestMethodDelete,
		http.MethodGet:     httpconv.RequestMethodGet,
		http.MethodHead:    httpconv.RequestMethodHead,
		http.MethodOptions: httpconv.RequestMethodOptions,
		http.MethodPatch:   httpconv.RequestMethodPatch,
		http.MethodPost:    httpconv.RequestMethodPost,
		http.MethodPut:     httpconv.RequestMethodPut,
		http.MethodTrace:   httpconv.RequestMethodTrace,
		"QUERY":            httpconv.RequestMethodQuery,
	}
}

func responseErrorType(status int, statusKnown bool, recovered any) string {
	if recovered == http.ErrAbortHandler {
		return "http.ErrAbortHandler"
	}
	if recovered != nil {
		return "panic"
	}
	if statusKnown && status >= http.StatusInternalServerError {
		return strconv.Itoa(status)
	}
	return ""
}

type otelRequestTimingKey struct{}

type otelRequestTiming struct {
	start time.Time
	end   time.Time
}

func otelTimingFromContext(ctx context.Context) *otelRequestTiming {
	timing, _ := ctx.Value(otelRequestTimingKey{}).(*otelRequestTiming)
	return timing
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

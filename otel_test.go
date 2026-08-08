package servekit_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	servekit "github.com/jaredjakacky/servekit"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

func TestTraceAndSpanIDFromContextReturnEmptyWithoutActiveSpan(t *testing.T) {
	t.Parallel()

	if got := servekit.TraceIDFromContext(context.Background()); got != "" {
		t.Fatalf("TraceIDFromContext(background) = %q, want empty", got)
	}
	if got := servekit.SpanIDFromContext(context.Background()); got != "" {
		t.Fatalf("SpanIDFromContext(background) = %q, want empty", got)
	}
}

func TestServerWithOpenTelemetryDisabledDoesNotInjectSpanContext(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	s := newOTelTestServer(
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithTracerProvider(tp),
	)
	s.HandleHTTP(http.MethodGet, "/ids", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s|%s", servekit.TraceIDFromContext(r.Context()), servekit.SpanIDFromContext(r.Context()))
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/ids")

	if got := rec.Body.String(); got != "|" {
		t.Fatalf("body = %q, want %q", got, "|")
	}
	if got := len(tp.Spans()); got != 0 {
		t.Fatalf("recorded spans = %d, want 0", got)
	}
}

func TestServerOpenTelemetryExtractsTraceContextAndExposesIDs(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithPropagator(propagation.TraceContext{}),
	)
	s.HandleHTTP(http.MethodGet, "/ids", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s|%s", servekit.TraceIDFromContext(r.Context()), servekit.SpanIDFromContext(r.Context()))
	}))

	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	parentSpanID, _ := trace.SpanIDFromHex("1112131415161718")
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     parentSpanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})

	req := httptest.NewRequest(http.MethodGet, "/ids", nil)
	propagation.TraceContext{}.Inject(trace.ContextWithRemoteSpanContext(req.Context(), parent), propagation.HeaderCarrier(req.Header))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if got := rec.Body.String(); got != traceID.String()+"|0000000000000001" {
		t.Fatalf("body = %q, want trace/span IDs with extracted trace", got)
	}

	spans := tp.Spans()
	if len(spans) != 1 {
		t.Fatalf("recorded spans = %d, want 1", len(spans))
	}
	if spans[0].Parent().TraceID() != traceID {
		t.Fatalf("parent trace ID = %s, want %s", spans[0].Parent().TraceID(), traceID)
	}
}

func TestServerOpenTelemetryAppliesSpanOptions(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithMeterProvider(mp),
		servekit.WithRouteLabeler(func(r *http.Request) string { return "custom.route" }),
		servekit.WithSpanNameFormatter(func(r *http.Request, route string) string { return "span:" + route }),
		servekit.WithOTelSpanAttributes(func(r *http.Request) []attribute.KeyValue {
			return []attribute.KeyValue{
				attribute.String("servekit.span_test", "yes"),
				attribute.String("servekit.active_span_id", servekit.SpanIDFromContext(r.Context())),
			}
		}),
		servekit.WithOTelMetricAttributes(func(r *http.Request) []attribute.KeyValue {
			return []attribute.KeyValue{attribute.String("servekit.metric_test", "yes")}
		}),
	)
	s.HandleHTTP(http.MethodGet, "/widgets", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/widgets")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	spans := tp.Spans()
	if len(spans) != 1 {
		t.Fatalf("recorded spans = %d, want 1", len(spans))
	}

	span := spans[0]
	if got := span.Name(); got != "span:custom.route" {
		t.Fatalf("span name = %q, want %q", got, "span:custom.route")
	}
	if got := span.AttributeValue("servekit.span_test"); got != "yes" {
		t.Fatalf("servekit.span_test attribute = %q, want %q", got, "yes")
	}
	if got := span.AttributeValue("servekit.metric_test"); got != nil {
		t.Fatalf("span servekit.metric_test attribute = %q, want omitted", got)
	}
	if got := span.AttributeValue("servekit.active_span_id"); got != "0000000000000001" {
		t.Fatalf("span attribute callback active span ID = %q, want server span ID", got)
	}
	if got := span.AttributeValue(string(semconv.HTTPRouteKey)); got != "custom.route" {
		t.Fatalf("http.route attribute = %q, want %q", got, "custom.route")
	}
	if got := span.AttributeValue(string(semconv.HTTPResponseStatusCodeKey)); got != int64(http.StatusCreated) {
		t.Fatalf("http.response.status_code attribute = %v, want %d", got, http.StatusCreated)
	}
	measurements := mp.float64Measurements("http.server.request.duration")
	if len(measurements) != 1 {
		t.Fatalf("duration measurements = %d, want 1", len(measurements))
	}
	if got := measurements[0].AttributeValue("servekit.metric_test"); got != "yes" {
		t.Fatalf("metric servekit.metric_test attribute = %q, want %q", got, "yes")
	}
	if got := measurements[0].AttributeValue("servekit.span_test"); got != nil {
		t.Fatalf("metric servekit.span_test attribute = %q, want omitted", got)
	}
}

func TestServerOpenTelemetryNormalizesUnknownRequestMethods(t *testing.T) {
	tp := newRecordingTracerProvider()
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithMeterProvider(mp),
	)
	s.HandleHTTP("BREW", "/widgets", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := performRequest(t, s.Handler(), "BREW", "/widgets")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	spans := tp.Spans()
	if len(spans) != 1 {
		t.Fatalf("recorded spans = %d, want 1", len(spans))
	}
	if got := spans[0].Name(); got != "HTTP /widgets" {
		t.Fatalf("span name = %q, want %q", got, "HTTP /widgets")
	}
	if got := spans[0].AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "_OTHER" {
		t.Fatalf("span http.request.method = %q, want %q", got, "_OTHER")
	}
	if got := spans[0].AttributeValue(string(semconv.HTTPRequestMethodOriginalKey)); got != "BREW" {
		t.Fatalf("span http.request.method_original = %q, want %q", got, "BREW")
	}

	measurements := mp.float64Measurements("http.server.request.duration")
	if len(measurements) != 1 {
		t.Fatalf("duration measurements = %d, want 1", len(measurements))
	}
	if got := measurements[0].AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "_OTHER" {
		t.Fatalf("metric http.request.method = %q, want %q", got, "_OTHER")
	}
	if got := measurements[0].AttributeValue(string(semconv.HTTPRequestMethodOriginalKey)); got != nil {
		t.Fatalf("metric http.request.method_original = %q, want omitted", got)
	}

	activeMeasurements := mp.int64Measurements("http.server.active_requests")
	if len(activeMeasurements) != 2 {
		t.Fatalf("active request measurements = %d, want 2", len(activeMeasurements))
	}
	for _, measurement := range activeMeasurements {
		if got := measurement.AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "_OTHER" {
			t.Errorf("active request http.request.method = %q, want %q", got, "_OTHER")
		}
		if got := measurement.AttributeValue(string(semconv.HTTPRequestMethodOriginalKey)); got != nil {
			t.Errorf("active request http.request.method_original = %q, want omitted", got)
		}
		if got := measurement.AttributeValue(string(semconv.URLSchemeKey)); got != "http" {
			t.Errorf("active request url.scheme = %q, want %q", got, "http")
		}
	}
}

func TestServerOpenTelemetryHonorsConfiguredKnownRequestMethods(t *testing.T) {
	t.Setenv("OTEL_INSTRUMENTATION_HTTP_KNOWN_METHODS", "BREW")

	tp := newRecordingTracerProvider()
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithMeterProvider(mp),
	)
	s.HandleHTTP("BREW", "/widgets", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	s.HandleHTTP(http.MethodGet, "/standard", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	performRequest(t, s.Handler(), "BREW", "/widgets")
	performRequest(t, s.Handler(), http.MethodGet, "/standard")

	spans := tp.Spans()
	if len(spans) != 2 {
		t.Fatalf("recorded spans = %d, want 2", len(spans))
	}
	span := spans[0]
	if got := span.Name(); got != "BREW /widgets" {
		t.Fatalf("span name = %q, want %q", got, "BREW /widgets")
	}
	if got := span.AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "BREW" {
		t.Fatalf("span http.request.method = %q, want %q", got, "BREW")
	}
	if got := span.AttributeValue(string(semconv.HTTPRequestMethodOriginalKey)); got != nil {
		t.Fatalf("span http.request.method_original = %q, want omitted", got)
	}
	if got := spans[1].AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "_OTHER" {
		t.Fatalf("replacement-list GET span http.request.method = %q, want %q", got, "_OTHER")
	}
	if got := spans[1].AttributeValue(string(semconv.HTTPRequestMethodOriginalKey)); got != http.MethodGet {
		t.Fatalf("replacement-list GET span http.request.method_original = %q, want %q", got, http.MethodGet)
	}

	measurements := mp.float64Measurements("http.server.request.duration")
	if len(measurements) != 2 {
		t.Fatalf("duration measurements = %d, want 2", len(measurements))
	}
	measurement := measurements[0]
	if got := measurement.AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "BREW" {
		t.Fatalf("metric http.request.method = %q, want %q", got, "BREW")
	}
	if got := measurements[1].AttributeValue(string(semconv.HTTPRequestMethodKey)); got != "_OTHER" {
		t.Fatalf("replacement-list GET metric http.request.method = %q, want %q", got, "_OTHER")
	}
}

func TestServerOpenTelemetryDerivesSchemeFromTransport(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithMeterProvider(mp),
	)
	s.HandleHTTP(http.MethodGet, "/scheme", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		configure  func(*http.Request)
		wantScheme string
	}{
		{
			name: "plain HTTP ignores forwarded and URL schemes",
			configure: func(r *http.Request) {
				r.URL.Scheme = "attacker-controlled"
				r.Header.Set("Forwarded", "for=192.0.2.1;proto=gopher")
				r.Header.Set("X-Forwarded-Proto", "also-attacker-controlled")
			},
			wantScheme: "http",
		},
		{
			name: "TLS ignores forwarded scheme",
			configure: func(r *http.Request) {
				r.TLS = &tls.ConnectionState{}
				r.Header.Set("Forwarded", "for=192.0.2.1;proto=http")
				r.Header.Set("X-Forwarded-Proto", "ftp")
			},
			wantScheme: "https",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/scheme", nil)
			tc.configure(req)
			s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		})
	}

	spans := tp.Spans()
	measurements := mp.float64Measurements("http.server.request.duration")
	activeMeasurements := mp.int64Measurements("http.server.active_requests")
	if len(spans) != len(tests) || len(measurements) != len(tests) || len(activeMeasurements) != 2*len(tests) {
		t.Fatalf(
			"spans/durations/active requests = %d/%d/%d, want %d/%d/%d",
			len(spans), len(measurements), len(activeMeasurements), len(tests), len(tests), 2*len(tests),
		)
	}
	for i, tc := range tests {
		if got := spans[i].AttributeValue(string(semconv.URLSchemeKey)); got != tc.wantScheme {
			t.Errorf("%s: span url.scheme = %q, want %q", tc.name, got, tc.wantScheme)
		}
		if got := measurements[i].AttributeValue(string(semconv.URLSchemeKey)); got != tc.wantScheme {
			t.Errorf("%s: metric url.scheme = %q, want %q", tc.name, got, tc.wantScheme)
		}
		for _, measurement := range activeMeasurements[2*i : 2*i+2] {
			if got := measurement.AttributeValue(string(semconv.URLSchemeKey)); got != tc.wantScheme {
				t.Errorf("%s: active request url.scheme = %q, want %q", tc.name, got, tc.wantScheme)
			}
		}
	}
}

func TestServerOpenTelemetryBoundsOutcomeMetricMethodAndScheme(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))
	s.HandleHTTP("BREW", "/secure", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called for unauthorized request")
	}), servekit.WithAuthCheck(func(*http.Request) bool { return false }))

	req := httptest.NewRequest("BREW", "/secure", nil)
	req.URL.Scheme = "attacker-controlled"
	req.Header.Set("Forwarded", "for=192.0.2.1;proto=gopher")
	req.Header.Set("X-Forwarded-Proto", "also-attacker-controlled")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	measurements := mp.int64Measurements("servekit.http.server.request.auth_rejection.count")
	assertInt64Measurement(t, measurements, 1, map[string]any{
		string(semconv.HTTPRequestMethodKey):      "_OTHER",
		string(semconv.URLSchemeKey):              "http",
		string(semconv.HTTPRouteKey):              "/secure",
		string(semconv.HTTPResponseStatusCodeKey): int64(http.StatusUnauthorized),
	})
	if got := measurements[0].AttributeValue(string(semconv.HTTPRequestMethodOriginalKey)); got != nil {
		t.Fatalf("outcome metric http.request.method_original = %q, want omitted", got)
	}
}

func TestServerOpenTelemetryUsesSemanticConventionRequestInstruments(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))
	s.HandleHTTP(http.MethodGet, "/widgets/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	performRequest(t, s.Handler(), http.MethodGet, "/widgets/123")

	duration, ok := mp.instrumentDescriptor("http.server.request.duration")
	if !ok {
		t.Fatal("http.server.request.duration instrument was not created")
	}
	if duration.kind != "float64 histogram" {
		t.Errorf("duration kind = %q, want %q", duration.kind, "float64 histogram")
	}
	if duration.description != "Duration of HTTP server requests." {
		t.Errorf("duration description = %q, want semantic-convention description", duration.description)
	}
	if duration.unit != "s" {
		t.Errorf("duration unit = %q, want %q", duration.unit, "s")
	}
	wantBuckets := []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	if !slices.Equal(duration.boundaries, wantBuckets) {
		t.Errorf("duration buckets = %v, want %v", duration.boundaries, wantBuckets)
	}

	active, ok := mp.instrumentDescriptor("http.server.active_requests")
	if !ok {
		t.Fatal("http.server.active_requests instrument was not created")
	}
	if active.kind != "int64 up-down counter" {
		t.Errorf("active requests kind = %q, want %q", active.kind, "int64 up-down counter")
	}
	if active.description != "Number of active HTTP server requests." {
		t.Errorf("active requests description = %q, want semantic-convention description", active.description)
	}
	if active.unit != "{request}" {
		t.Errorf("active requests unit = %q, want %q", active.unit, "{request}")
	}
	activeMeasurements := mp.int64Measurements("http.server.active_requests")
	if len(activeMeasurements) != 2 {
		t.Fatalf("active request measurements = %d, want 2", len(activeMeasurements))
	}
	for _, measurement := range activeMeasurements {
		if got := measurement.AttributeValue(string(semconv.HTTPRequestMethodKey)); got != http.MethodGet {
			t.Errorf("active request http.request.method = %q, want %q", got, http.MethodGet)
		}
		if got := measurement.AttributeValue(string(semconv.URLSchemeKey)); got != "http" {
			t.Errorf("active request url.scheme = %q, want %q", got, "http")
		}
		if got := measurement.AttributeValue(string(semconv.HTTPRouteKey)); got != nil {
			t.Errorf("active request http.route = %q, want omitted", got)
		}
	}
	durationMeasurements := mp.float64Measurements("http.server.request.duration")
	if len(durationMeasurements) != 1 {
		t.Fatalf("duration measurements = %d, want 1", len(durationMeasurements))
	}
	if got := durationMeasurements[0].AttributeValue(string(semconv.HTTPRouteKey)); got != "/widgets/{id}" {
		t.Errorf("duration http.route = %q, want route template %q", got, "/widgets/{id}")
	}

	for _, obsolete := range []string{"http.server.request.count", "http.server.request.in_flight"} {
		if _, ok := mp.instrumentDescriptor(obsolete); ok {
			t.Errorf("obsolete nonstandard instrument %q was created", obsolete)
		}
	}
}

func TestServerSkipTelemetrySuppressesSpansForEndpoint(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	s := newOTelTestServer(servekit.WithTracerProvider(tp))
	s.HandleHTTP(http.MethodGet, "/skip", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), servekit.WithSkipTelemetry())
	s.HandleHTTP(http.MethodGet, "/keep", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	performRequest(t, s.Handler(), http.MethodGet, "/skip")
	if got := len(tp.Spans()); got != 0 {
		t.Fatalf("recorded spans after skipped endpoint = %d, want 0", got)
	}

	performRequest(t, s.Handler(), http.MethodGet, "/keep")
	if got := len(tp.Spans()); got != 1 {
		t.Fatalf("recorded spans after non-skipped endpoint = %d, want 1", got)
	}
}

func TestServerOpenTelemetryHandlerModeRecordsRequestMetricsWithoutConnectionMetrics(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))
	s.HandleHTTP(http.MethodGet, "/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	waitForHTTPStatus(t, ts.URL+"/metrics", http.StatusCreated, 500*time.Millisecond)

	if got := len(mp.float64Measurements("http.server.request.duration")); got == 0 {
		t.Fatal("http.server.request.duration measurements = 0, want duration metrics on Handler() path")
	}
	if got := len(mp.int64Measurements("servekit.http.server.connection.active")); got != 0 {
		t.Fatalf("servekit.http.server.connection.active measurements = %d, want none when only Handler() is mounted into an outer server", got)
	}
}

func TestServerOpenTelemetryRunPathRecordsConnectionMetrics(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithAddr(addr),
		servekit.WithMeterProvider(mp),
	)
	s.HandleHTTP(http.MethodGet, "/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	waitForHTTPStatus(t, "http://"+addr+"/metrics", http.StatusNoContent, 2*time.Second)
	cancel()

	if err := waitForRunResult(t, errCh, 2*time.Second); err != nil {
		t.Fatalf("Run() error = %v, want nil on context cancellation", err)
	}
	if got := len(mp.float64Measurements("http.server.request.duration")); got == 0 {
		t.Fatal("http.server.request.duration measurements = 0, want request metrics on Run() path")
	}
	if got := len(mp.int64Measurements("servekit.http.server.connection.active")); got == 0 {
		t.Fatal("servekit.http.server.connection.active measurements = 0, want connection metrics on Run() path")
	}
}

func TestServerOpenTelemetryRecordsAuthRejectionMetric(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))
	s.Handle(http.MethodGet, "/secure", func(r *http.Request) (any, error) {
		t.Fatal("handler called for unauthorized request")
		return nil, nil
	}, servekit.WithAuthCheck(func(*http.Request) bool { return false }))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/secure")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	assertInt64Measurement(
		t,
		mp.int64Measurements("servekit.http.server.request.auth_rejection.count"),
		1,
		map[string]any{
			string(semconv.HTTPRequestMethodKey):      http.MethodGet,
			string(semconv.HTTPRouteKey):              "/secure",
			string(semconv.HTTPResponseStatusCodeKey): int64(http.StatusUnauthorized),
		},
	)
}

func TestServerOpenTelemetryRecordsTimeoutMetric(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))
	s.Handle(http.MethodGet, "/timeout", func(r *http.Request) (any, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}, servekit.WithEndpointTimeout(10*time.Millisecond))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/timeout")
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}

	assertInt64Measurement(
		t,
		mp.int64Measurements("servekit.http.server.request.timeout.count"),
		1,
		map[string]any{
			string(semconv.HTTPRequestMethodKey):      http.MethodGet,
			string(semconv.HTTPRouteKey):              "/timeout",
			string(semconv.HTTPResponseStatusCodeKey): int64(http.StatusGatewayTimeout),
			string(semconv.ErrorTypeKey):              "504",
		},
	)
}

func TestServerOpenTelemetryRecordsCancellationMetric(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))

	started := make(chan struct{})
	s.Handle(http.MethodGet, "/cancel", func(r *http.Request) (any, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cancel", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, req)
		close(done)
	}()

	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ServeHTTP did not return after request cancellation")
	}

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}

	assertInt64Measurement(
		t,
		mp.int64Measurements("servekit.http.server.request.cancellation.count"),
		1,
		map[string]any{
			string(semconv.HTTPRequestMethodKey):      http.MethodGet,
			string(semconv.HTTPRouteKey):              "/cancel",
			string(semconv.HTTPResponseStatusCodeKey): int64(http.StatusGatewayTimeout),
			string(semconv.ErrorTypeKey):              "504",
		},
	)
}

func TestServerOpenTelemetryPanicMetricCanBeDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		opts           []servekit.Option
		wantPanicCount int
	}{
		{name: "enabled by default", wantPanicCount: 1},
		{
			name:           "disabled explicitly",
			opts:           []servekit.Option{servekit.WithOTelPanicMetricEnabled(false)},
			wantPanicCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tp := newRecordingTracerProvider()
			mp := newRecordingMeterProvider()
			opts := append([]servekit.Option{
				servekit.WithTracerProvider(tp),
				servekit.WithMeterProvider(mp),
			}, tc.opts...)
			s := newOTelTestServer(opts...)
			s.HandleHTTP(http.MethodGet, "/panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				panic("boom")
			}))

			rec := performRequest(t, s.Handler(), http.MethodGet, "/panic")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
			}

			got := mp.int64Measurements("servekit.http.server.request.panic.count")
			if len(got) != tc.wantPanicCount {
				t.Fatalf("panic count measurements = %d, want %d", len(got), tc.wantPanicCount)
			}
			if tc.wantPanicCount == 1 {
				assertInt64Measurement(
					t,
					got,
					1,
					map[string]any{
						string(semconv.HTTPRequestMethodKey):      http.MethodGet,
						string(semconv.HTTPRouteKey):              "/panic",
						string(semconv.HTTPResponseStatusCodeKey): int64(http.StatusInternalServerError),
						string(semconv.ErrorTypeKey):              "panic",
					},
				)
			}
			spans := tp.Spans()
			if len(spans) != 1 {
				t.Fatalf("recorded spans = %d, want 1", len(spans))
			}
			if got := spans[0].AttributeValue(string(semconv.ErrorTypeKey)); got != "panic" {
				t.Fatalf("span error.type = %q, want %q", got, "panic")
			}
			if spans[0].status != codes.Error {
				t.Fatalf("span status = %v, want %v", spans[0].status, codes.Error)
			}
		})
	}
}

func TestServerOpenTelemetryDoesNotInventStatusForUncommittedAbort(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithMeterProvider(mp),
	)
	s.HandleHTTP(http.MethodGet, "/abort", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/abort", nil)

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recovered panic = %v, want %v", recovered, http.ErrAbortHandler)
		}
		measurements := mp.float64Measurements("http.server.request.duration")
		if len(measurements) != 1 {
			t.Fatalf("request count measurements = %d, want 1", len(measurements))
		}
		if got := measurements[0].AttributeValue(string(semconv.HTTPResponseStatusCodeKey)); got != nil {
			t.Fatalf("metric http.response.status_code = %v, want omitted for uncommitted abort", got)
		}
		if got := measurements[0].AttributeValue(string(semconv.ErrorTypeKey)); got != "http.ErrAbortHandler" {
			t.Fatalf("metric error.type = %v, want %q", got, "http.ErrAbortHandler")
		}
		if got := len(mp.int64Measurements("servekit.http.server.request.panic.count")); got != 0 {
			t.Fatalf("panic count measurements = %d, want 0 for http.ErrAbortHandler", got)
		}
		spans := tp.Spans()
		if len(spans) != 1 {
			t.Fatalf("recorded spans = %d, want 1", len(spans))
		}
		if got := spans[0].AttributeValue(string(semconv.HTTPResponseStatusCodeKey)); got != nil {
			t.Fatalf("span http.response.status_code = %v, want omitted for uncommitted abort", got)
		}
		if got := spans[0].AttributeValue(string(semconv.ErrorTypeKey)); got != "http.ErrAbortHandler" {
			t.Fatalf("span error.type = %v, want %q", got, "http.ErrAbortHandler")
		}
	}()

	s.Handler().ServeHTTP(rec, req)
}

func TestServerOpenTelemetryCommittedPanicRetainsObservedStatus(t *testing.T) {
	t.Parallel()

	mp := newRecordingMeterProvider()
	s := newOTelTestServer(servekit.WithMeterProvider(mp))
	s.HandleHTTP(http.MethodGet, "/panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "partial")
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)

	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recovered panic = %v, want %v", recovered, http.ErrAbortHandler)
		}
		wantAttrs := map[string]any{
			string(semconv.HTTPRequestMethodKey):      http.MethodGet,
			string(semconv.HTTPRouteKey):              "/panic",
			string(semconv.HTTPResponseStatusCodeKey): int64(http.StatusAccepted),
			string(semconv.ErrorTypeKey):              "panic",
		}
		assertFloat64Measurement(t, mp.float64Measurements("http.server.request.duration"), wantAttrs)
		assertInt64Measurement(t, mp.int64Measurements("servekit.http.server.request.panic.count"), 1, wantAttrs)
	}()

	s.Handler().ServeHTTP(rec, req)
}

func TestServerOpenTelemetryRunPathTracksHijackedConnections(t *testing.T) {
	addr := reserveLoopbackAddr(t)
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithAddr(addr),
		servekit.WithMeterProvider(mp),
		servekit.WithDefaultEndpointsEnabled(true),
	)
	s.HandleHTTP(http.MethodGet, "/hijack", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacker missing", http.StatusInternalServerError)
			return
		}

		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = rw.WriteString("Content-Type: text/plain\r\n")
		_, _ = rw.WriteString("Connection: close\r\n")
		_, _ = rw.WriteString("Content-Length: 8\r\n")
		_, _ = rw.WriteString("\r\n")
		_, _ = rw.WriteString("hijacked")
		_ = rw.Flush()
		_ = conn.Close()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Run(ctx)
	}()

	waitForHTTPStatus(t, "http://"+addr+"/readyz", http.StatusOK, 2*time.Second)

	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("dial hijack route: %v", err)
	}
	_, _ = fmt.Fprintf(conn, "GET /hijack HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", addr)
	body, err := io.ReadAll(conn)
	_ = conn.Close()
	if err != nil {
		t.Fatalf("read hijacked response: %v", err)
	}
	if !bytes.Contains(body, []byte("hijacked")) {
		t.Fatalf("hijacked response = %q, want body %q", string(body), "hijacked")
	}

	measurements := waitForInt64MeasurementCount(t, mp, "servekit.http.server.connection.hijacked.active", 2, 500*time.Millisecond)
	assertInt64Measurement(t, measurements, 1, nil)
	assertInt64Measurement(t, measurements, -1, nil)

	cancel()
	if err := waitForRunResult(t, errCh, 2*time.Second); err != nil {
		t.Fatalf("Run() error = %v, want nil on context cancellation", err)
	}
}

func TestServerOpenTelemetryDoesNotInventStatusForHijackedResponse(t *testing.T) {
	t.Parallel()

	tp := newRecordingTracerProvider()
	mp := newRecordingMeterProvider()
	s := newOTelTestServer(
		servekit.WithTracerProvider(tp),
		servekit.WithMeterProvider(mp),
	)
	s.HandleHTTP(http.MethodGet, "/hijack", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		_ = conn.Close()
	}))

	writer := newHijackRecorder()
	req := httptest.NewRequest(http.MethodGet, "/hijack", nil)
	s.Handler().ServeHTTP(writer, req)

	spans := tp.Spans()
	if len(spans) != 1 {
		t.Fatalf("recorded spans = %d, want 1", len(spans))
	}
	if got := spans[0].AttributeValue(string(semconv.HTTPResponseStatusCodeKey)); got != nil {
		t.Fatalf("span http.response.status_code = %v, want omitted for unobserved hijacked response", got)
	}

	measurements := mp.float64Measurements("http.server.request.duration")
	if len(measurements) != 1 {
		t.Fatalf("request count measurements = %d, want 1", len(measurements))
	}
	if got := measurements[0].AttributeValue(string(semconv.HTTPResponseStatusCodeKey)); got != nil {
		t.Fatalf("metric http.response.status_code = %v, want omitted for unobserved hijacked response", got)
	}
}

func newOTelTestServer(opts ...servekit.Option) *servekit.Server {
	base := []servekit.Option{
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithAccessLogEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	}
	base = append(base, opts...)
	return servekit.New(base...)
}

type recordingMeterProvider struct {
	metricnoop.MeterProvider
	mu            sync.Mutex
	int64ByName   map[string][]int64Measurement
	float64ByName map[string][]float64Measurement
	descriptors   map[string]instrumentDescriptor
}

func newRecordingMeterProvider() *recordingMeterProvider {
	return &recordingMeterProvider{
		int64ByName:   make(map[string][]int64Measurement),
		float64ByName: make(map[string][]float64Measurement),
		descriptors:   make(map[string]instrumentDescriptor),
	}
}

func (p *recordingMeterProvider) Meter(string, ...metric.MeterOption) metric.Meter {
	return &recordingMeter{provider: p}
}

func (p *recordingMeterProvider) recordInt64(name string, value int64, opts ...metric.AddOption) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg := metric.NewAddConfig(opts)
	p.int64ByName[name] = append(p.int64ByName[name], int64Measurement{
		value: value,
		attrs: cloneMeasurementAttrs(cfg.Attributes()),
	})
}

func (p *recordingMeterProvider) recordFloat64(name string, value float64, opts ...metric.RecordOption) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg := metric.NewRecordConfig(opts)
	p.float64ByName[name] = append(p.float64ByName[name], float64Measurement{
		value: value,
		attrs: cloneMeasurementAttrs(cfg.Attributes()),
	})
}

func (p *recordingMeterProvider) int64Measurements(name string) []int64Measurement {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int64Measurement, len(p.int64ByName[name]))
	copy(out, p.int64ByName[name])
	return out
}

func (p *recordingMeterProvider) float64Measurements(name string) []float64Measurement {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]float64Measurement, len(p.float64ByName[name]))
	copy(out, p.float64ByName[name])
	return out
}

func (p *recordingMeterProvider) recordInstrument(name string, descriptor instrumentDescriptor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.descriptors[name] = descriptor
}

func (p *recordingMeterProvider) instrumentDescriptor(name string) (instrumentDescriptor, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	descriptor, ok := p.descriptors[name]
	descriptor.boundaries = append([]float64(nil), descriptor.boundaries...)
	return descriptor, ok
}

type instrumentDescriptor struct {
	kind        string
	description string
	unit        string
	boundaries  []float64
}

type recordingMeter struct {
	metricnoop.Meter
	provider *recordingMeterProvider
}

func (m *recordingMeter) Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	cfg := metric.NewInt64CounterConfig(opts...)
	m.provider.recordInstrument(name, instrumentDescriptor{
		kind:        "int64 counter",
		description: cfg.Description(),
		unit:        cfg.Unit(),
	})
	return &recordingInt64Counter{provider: m.provider, name: name}, nil
}

func (m *recordingMeter) Int64UpDownCounter(name string, opts ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	cfg := metric.NewInt64UpDownCounterConfig(opts...)
	m.provider.recordInstrument(name, instrumentDescriptor{
		kind:        "int64 up-down counter",
		description: cfg.Description(),
		unit:        cfg.Unit(),
	})
	return &recordingInt64UpDownCounter{provider: m.provider, name: name}, nil
}

func (m *recordingMeter) Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	cfg := metric.NewFloat64HistogramConfig(opts...)
	m.provider.recordInstrument(name, instrumentDescriptor{
		kind:        "float64 histogram",
		description: cfg.Description(),
		unit:        cfg.Unit(),
		boundaries:  append([]float64(nil), cfg.ExplicitBucketBoundaries()...),
	})
	return &recordingFloat64Histogram{provider: m.provider, name: name}, nil
}

type recordingInt64Counter struct {
	metricnoop.Int64Counter
	provider *recordingMeterProvider
	name     string
}

func (c *recordingInt64Counter) Add(_ context.Context, incr int64, opts ...metric.AddOption) {
	c.provider.recordInt64(c.name, incr, opts...)
}

func (c *recordingInt64Counter) Enabled(context.Context) bool { return true }

type recordingInt64UpDownCounter struct {
	metricnoop.Int64UpDownCounter
	provider *recordingMeterProvider
	name     string
}

func (c *recordingInt64UpDownCounter) Add(_ context.Context, incr int64, opts ...metric.AddOption) {
	c.provider.recordInt64(c.name, incr, opts...)
}

func (c *recordingInt64UpDownCounter) Enabled(context.Context) bool { return true }

type recordingFloat64Histogram struct {
	metricnoop.Float64Histogram
	provider *recordingMeterProvider
	name     string
}

func (h *recordingFloat64Histogram) Record(_ context.Context, value float64, opts ...metric.RecordOption) {
	h.provider.recordFloat64(h.name, value, opts...)
}

func (h *recordingFloat64Histogram) Enabled(context.Context) bool { return true }

type recordingTracerProvider struct {
	embedded.TracerProvider
	mu      sync.Mutex
	counter uint64
	spans   []*recordingSpan
}

func newRecordingTracerProvider() *recordingTracerProvider {
	return &recordingTracerProvider{}
}

func (p *recordingTracerProvider) Tracer(string, ...trace.TracerOption) trace.Tracer {
	return recordingTracer{provider: p}
}

func (p *recordingTracerProvider) nextSpanContext(parent trace.SpanContext) trace.SpanContext {
	p.counter++
	spanID, _ := trace.SpanIDFromHex(fmt.Sprintf("%016x", p.counter))
	traceID := parent.TraceID()
	if !traceID.IsValid() {
		traceID, _ = trace.TraceIDFromHex("100f0e0d0c0b0a090807060504030201")
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
}

func (p *recordingTracerProvider) recordSpan(span *recordingSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spans = append(p.spans, span)
}

func (p *recordingTracerProvider) Spans() []*recordingSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*recordingSpan, len(p.spans))
	copy(out, p.spans)
	return out
}

type recordingTracer struct {
	embedded.Tracer
	provider *recordingTracerProvider
}

func (t recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	parent := trace.SpanContextFromContext(ctx)
	cfg := trace.NewSpanStartConfig(opts...)
	span := &recordingSpan{
		provider: t.provider,
		parent:   parent,
		name:     name,
		attrs:    append([]attribute.KeyValue(nil), cfg.Attributes()...),
		spanCtx:  t.provider.nextSpanContext(parent),
	}
	t.provider.recordSpan(span)
	return trace.ContextWithSpan(ctx, span), span
}

type recordingSpan struct {
	embedded.Span
	provider *recordingTracerProvider
	parent   trace.SpanContext
	spanCtx  trace.SpanContext
	name     string
	attrs    []attribute.KeyValue
	status   codes.Code
}

func (s *recordingSpan) End(...trace.SpanEndOption) {}

func (s *recordingSpan) AddEvent(string, ...trace.EventOption) {}

func (s *recordingSpan) AddLink(trace.Link) {}

func (s *recordingSpan) IsRecording() bool { return true }

func (s *recordingSpan) RecordError(error, ...trace.EventOption) {}

func (s *recordingSpan) SpanContext() trace.SpanContext { return s.spanCtx }

func (s *recordingSpan) SetStatus(code codes.Code, description string) { s.status = code }

func (s *recordingSpan) SetName(name string) { s.name = name }

func (s *recordingSpan) SetAttributes(kv ...attribute.KeyValue) { s.attrs = append(s.attrs, kv...) }

func (s *recordingSpan) TracerProvider() trace.TracerProvider { return s.provider }

func (s *recordingSpan) Parent() trace.SpanContext { return s.parent }

func (s *recordingSpan) Name() string { return s.name }

func (s *recordingSpan) AttributeValue(key string) any {
	for i := len(s.attrs) - 1; i >= 0; i-- {
		if string(s.attrs[i].Key) == key {
			return valueAsAny(s.attrs[i].Value)
		}
	}
	return nil
}

func valueAsAny(v attribute.Value) any {
	switch v.Type() {
	case attribute.BOOL:
		return v.AsBool()
	case attribute.INT64:
		return v.AsInt64()
	case attribute.STRING:
		return v.AsString()
	default:
		return fmt.Sprint(v.AsInterface())
	}
}

type int64Measurement struct {
	value int64
	attrs []attribute.KeyValue
}

func (m int64Measurement) AttributeValue(key string) any {
	for _, kv := range m.attrs {
		if string(kv.Key) == key {
			return valueAsAny(kv.Value)
		}
	}
	return nil
}

type float64Measurement struct {
	value float64
	attrs []attribute.KeyValue
}

func (m float64Measurement) AttributeValue(key string) any {
	for _, kv := range m.attrs {
		if string(kv.Key) == key {
			return valueAsAny(kv.Value)
		}
	}
	return nil
}

func cloneMeasurementAttrs(set attribute.Set) []attribute.KeyValue {
	attrs := set.ToSlice()
	out := make([]attribute.KeyValue, len(attrs))
	copy(out, attrs)
	return out
}

func assertInt64Measurement(t *testing.T, measurements []int64Measurement, wantValue int64, wantAttrs map[string]any) {
	t.Helper()

	for _, measurement := range measurements {
		if measurement.value != wantValue {
			continue
		}
		matched := true
		for key, want := range wantAttrs {
			if got := measurement.AttributeValue(key); got != want {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}

	t.Fatalf("measurements = %#v, want value %d with attrs %v", measurements, wantValue, wantAttrs)
}

func assertFloat64Measurement(t *testing.T, measurements []float64Measurement, wantAttrs map[string]any) {
	t.Helper()

	for _, measurement := range measurements {
		matched := true
		for key, want := range wantAttrs {
			if got := measurement.AttributeValue(key); got != want {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}

	t.Fatalf("measurements = %#v, want attrs %v", measurements, wantAttrs)
}

func waitForInt64MeasurementCount(t *testing.T, mp *recordingMeterProvider, name string, want int, timeout time.Duration) []int64Measurement {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		measurements := mp.int64Measurements(name)
		if len(measurements) >= want {
			return measurements
		}
		time.Sleep(10 * time.Millisecond)
	}

	measurements := mp.int64Measurements(name)
	t.Fatalf("%s measurements = %d, want at least %d", name, len(measurements), want)
	return nil
}

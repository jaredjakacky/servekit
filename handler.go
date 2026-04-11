package servekit

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// HandlerFunc is the function form accepted by Server.Handle.
//
// The request passed to HandlerFunc carries any endpoint timeout set with
// WithEndpointTimeout in r.Context(). Returning a non-nil error delegates
// response writing to the server ErrorEncoder.
type HandlerFunc func(r *http.Request) (any, error)

// Handle registers a method/path endpoint backed by a HandlerFunc.
//
// Handle applies endpoint options in this order: timeout and auth gate before
// invoking h with an updated request context, then success encoding with the
// server ResponseEncoder unless WithEndpointResponseEncoder overrides it.
// Errors from h or the encoder are sent through the server ErrorEncoder. If a
// success response has already been committed, the error path may not be able
// to replace it cleanly.
func (s *Server) Handle(method, path string, h HandlerFunc, opts ...EndpointOption) {
	cfg := endpointConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := h(r)
		if err != nil {
			_ = s.errorResponse(w, r, err)
			return
		}
		encoder := s.responseEncoder
		if cfg.responseOverride != nil {
			encoder = cfg.responseOverride
		}
		if err := encoder(w, r, payload); err != nil {
			_ = s.errorResponse(w, r, err)
		}
	})
	wrapped := s.wrapEndpoint(base, cfg)
	final := Chain(wrapped, cfg.middlewares...)
	s.register(method, path, final, cfg)
}

// HandleHTTP registers a method/path endpoint backed by a raw http.Handler.
//
// HandleHTTP applies endpoint options in this order: timeout and auth gate
// before invoking h, then endpoint middleware. Use HandleHTTP when you need
// direct control over response writing while still using Servekit middleware
// composition and endpoint options.
//
// Optional writer capabilities such as Flush and Hijack are not guaranteed by
// http.ResponseWriter itself. They depend on what the underlying concrete
// writer supports at runtime. Servekit preserves those capabilities when they
// are present so HandleHTTP remains a credible raw escape hatch for streaming,
// upgrade, proxy, and other raw-response use cases.
func (s *Server) HandleHTTP(method, path string, h http.Handler, opts ...EndpointOption) {
	cfg := endpointConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	wrapped := s.wrapEndpoint(h, cfg)
	final := Chain(wrapped, cfg.middlewares...)
	s.register(method, path, final, cfg)
}

// wrapEndpoint applies per-endpoint timeout and auth behavior around h.
//
// The returned handler updates the request context before invoking h so both
// Handle and HandleHTTP observe the same endpoint-level policy.
func (s *Server) wrapEndpoint(h http.Handler, cfg endpointConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if cfg.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
			defer cancel()
		}
		r = r.WithContext(ctx)
		effectiveLimit := s.requestBodyLimit
		if cfg.bodyLimit != 0 {
			effectiveLimit = cfg.bodyLimit
		}
		if effectiveLimit > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, effectiveLimit)
		}
		if cfg.requireAuth != nil && !cfg.requireAuth(r) {
			markRequestAuthRejected(r)
			_ = s.errorResponse(w, r, HTTPError{StatusCode: http.StatusUnauthorized, Message: "unauthorized"})
			return
		}
		if cfg.requireAuthGate != nil {
			if err := cfg.requireAuthGate(r); err != nil {
				markRequestAuthRejected(r)
				_ = s.errorResponse(w, r, err)
				return
			}
		}
		h.ServeHTTP(w, r)
		switch ctx.Err() {
		case context.DeadlineExceeded:
			markRequestTimedOut(r)
		case context.Canceled:
			markRequestCanceled(r)
		}
	})
}

// register validates and installs a fully prepared route into the server mux.
//
// The wrapper records the matched path pattern so outer observability
// middleware can observe the final route after mux dispatch has run.
func (s *Server) register(method, path string, h http.Handler, cfg endpointConfig) {
	validateRoute(method, path)
	pattern := method + " " + path
	base := h
	h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Seed the matched-route holder here so access logs and other outer
		// middleware can observe the final route even when OTel is disabled.
		r = withMatchedRoute(r)
		setMatchedRoutePath(r, path)
		base.ServeHTTP(w, r)
	})
	if cfg.skipTelemetry {
		if s.skipTelemetryPatterns == nil {
			s.skipTelemetryPatterns = make(map[string]struct{})
		}
		s.skipTelemetryPatterns[pattern] = struct{}{}
		h = Chain(h, Middleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, markSkipTelemetry(r))
			})
		}))
	}
	if cfg.skipAccessLog {
		h = Chain(h, SkipAccessLog())
	}
	s.mux.Handle(pattern, h)
}

// validateRoute rejects obviously broken route definitions at registration time.
func validateRoute(method, path string) {
	if method == "" {
		panic("servekit: route method must not be empty")
	}
	if path == "" {
		panic("servekit: route path must not be empty")
	}
}

// ReadinessCheck is invoked by the built-in /readyz endpoint.
//
// Returning nil marks the dependency healthy. Returning an error marks the
// service not ready and includes the error text in the response payload.
type ReadinessCheck func(context.Context) error

// HTTPError carries an HTTP status code alongside an underlying error.
//
// Use HTTPError (or Error) when handlers need explicit control over status
// mapping instead of relying on the default 500/504 behavior.
type HTTPError struct {
	StatusCode int    // StatusCode is the HTTP status returned for this error.
	Message    string // Message is the client-facing error text.
	Err        error  // Err is the wrapped underlying cause, when present.
}

// Error implements error.
func (e HTTPError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap returns the wrapped cause for errors.Is/errors.As.
func (e HTTPError) Unwrap() error {
	return e.Err
}

// Error constructs an HTTPError value.
func Error(status int, message string, err error) error {
	return HTTPError{StatusCode: status, Message: message, Err: err}
}

func statusFromError(err error) int {
	var httpErr HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode > 0 {
		return httpErr.StatusCode
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return http.StatusGatewayTimeout
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusInternalServerError
}

// EndpointOption configures per-endpoint behavior for Handle and HandleHTTP.
type EndpointOption func(*endpointConfig)

// endpointConfig holds the accumulated endpoint option state during route
// registration.
type endpointConfig struct {
	timeout          time.Duration
	bodyLimit        int64
	middlewares      []Middleware
	requireAuth      func(*http.Request) bool
	requireAuthGate  func(*http.Request) error
	responseOverride ResponseEncoder
	skipAccessLog    bool
	skipTelemetry    bool
}

// WithEndpointMiddleware appends middleware applied only to that endpoint.
//
// Endpoint middleware wraps the handler before global server middleware is
// applied by Server.Handler.
func WithEndpointMiddleware(mw ...Middleware) EndpointOption {
	return func(cfg *endpointConfig) { cfg.middlewares = append(cfg.middlewares, mw...) }
}

// WithEndpointTimeout sets a per-endpoint context timeout.
//
// A timeout of zero leaves the incoming request context unchanged.
func WithEndpointTimeout(timeout time.Duration) EndpointOption {
	return func(cfg *endpointConfig) { cfg.timeout = timeout }
}

// WithBodyLimit sets the maximum number of bytes Servekit will read from
// the request body for this endpoint. A value of -1 disables the limit
// entirely. The default is the server-wide WithRequestBodyLimit value
// (4 MiB unless overridden).
//
// When the limit is exceeded, net/http returns an *http.MaxBytesError and
// Servekit maps it to HTTP 413 Request Entity Too Large.
func WithBodyLimit(n int64) EndpointOption {
	return func(cfg *endpointConfig) { cfg.bodyLimit = n }
}

// WithAuthCheck installs an authorization gate for the endpoint.
//
// When check returns false, Handle and HandleHTTP respond with HTTP 401 via the
// current ErrorEncoder and do not invoke the handler. This convenience form
// always returns HTTP 401. Use WithAuthGate when you need control over the
// returned status or message.
func WithAuthCheck(check func(*http.Request) bool) EndpointOption {
	return func(cfg *endpointConfig) { cfg.requireAuth = check }
}

// WithAuthGate installs an error-returning auth gate for the endpoint.
//
// When fn returns a non-nil error, Handle and HandleHTTP pass that error
// directly to the current ErrorEncoder and do not invoke the handler. Return
// HTTPError values or Error(...) when you need explicit control over the
// response status and message.
func WithAuthGate(fn func(*http.Request) error) EndpointOption {
	return func(cfg *endpointConfig) { cfg.requireAuthGate = fn }
}

// WithEndpointResponseEncoder overrides success encoding for one endpoint.
//
// This option applies only to Handle. If the encoder returns an error, that
// error is delegated to the server ErrorEncoder.
func WithEndpointResponseEncoder(encoder ResponseEncoder) EndpointOption {
	return func(cfg *endpointConfig) { cfg.responseOverride = encoder }
}

// WithSkipAccessLog suppresses AccessLog output for one endpoint.
//
// This is useful for high-frequency probes such as /healthz and /readyz.
func WithSkipAccessLog() EndpointOption {
	return func(cfg *endpointConfig) { cfg.skipAccessLog = true }
}

// WithSkipTelemetry suppresses built-in OTel tracing and metrics for one endpoint.
func WithSkipTelemetry() EndpointOption {
	return func(cfg *endpointConfig) { cfg.skipTelemetry = true }
}

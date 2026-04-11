package servekit

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"
)

type contextKey string

const requestIDKey contextKey = "servekit_request_id"
const correlationIDKey contextKey = "servekit_correlation_id"
const skipLogKey contextKey = "servekit_skip_log"
const skipTelemetryKey contextKey = "servekit_skip_telemetry"
const matchedRouteKey contextKey = "servekit_matched_route"
const requestOutcomeKey contextKey = "servekit_request_outcome"

// matchedRoute holds the mux-resolved path pattern for a single request.
//
// Outer observability middleware starts before mux matching runs. Because inner
// middleware may replace the *http.Request via WithContext, the outer layer
// cannot rely on its local request variable to observe the mux-populated
// Pattern field later. A shared per-request holder lets the matched route flow
// back out after dispatch completes.
type matchedRoute struct {
	path string
}

type skipLogState struct {
	skip bool
}

type skipTelemetryState struct {
	skip bool
}

// requestOutcome carries per-request signals discovered deeper in the handler
// stack so outer metrics middleware can label and count them after dispatch.
type requestOutcome struct {
	timedOut     bool
	canceled     bool
	authRejected bool
}

var requestCounter atomic.Uint64

// RequestID ensures each request has an X-Request-ID value.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if id == "" {
				id = newCorrelationValue()
			}
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CorrelationID ensures each request has an X-Correlation-ID value.
func CorrelationID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Correlation-ID")
			if id == "" {
				if requestID := RequestIDFromContext(r.Context()); requestID != "" {
					id = requestID
				} else {
					id = newCorrelationValue()
				}
			}
			ctx := context.WithValue(r.Context(), correlationIDKey, id)
			w.Header().Set("X-Correlation-ID", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessLog logs one structured entry per completed request.
//
// The remote_addr field reflects the direct TCP peer address from
// http.Request.RemoteAddr. Trusting proxy headers is the responsibility of
// upstream middleware, not Servekit's access logger.
func AccessLog(logger *slog.Logger) Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			r = withSkipLogState(r)
			rw := captureWriter(w, responseCaptureHooks{})
			defer func() {
				rec := recover()
				if !skipAccessLogRequested(r) {
					// bytes is the response body byte count observed by the wrapper. It
					// intentionally excludes headers and wire/protocol overhead.
					args := []any{
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.Int("status", completedStatusCode(rw, rec != nil)),
						slog.Int("bytes", rw.BytesWritten()),
						slog.Duration("duration", time.Since(start)),
					}
					if route := matchedRoutePath(r); route != "" {
						args = append(args, slog.String("route", route))
					}
					args = append(args,
						slog.String("remote_addr", r.RemoteAddr),
						slog.String("request_id", RequestIDFromContext(r.Context())),
						slog.String("correlation_id", CorrelationIDFromContext(r.Context())),
						slog.String("trace_id", TraceIDFromContext(r.Context())),
						slog.String("span_id", SpanIDFromContext(r.Context())),
					)
					logger.Info("request", args...)
				}
				if rec != nil {
					panic(rec)
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// SkipAccessLog marks a request so AccessLog omits its log entry.
func SkipAccessLog() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, markSkipAccessLog(r))
		})
	}
}

// Recovery logs panics and then applies one of two mutually exclusive
// strategies.
//
// By default, with propagate set to false, Recovery uses contain-and-continue
// behavior: it logs the original panic value and stack trace, writes a
// best-effort JSON 500 in Servekit's default error shape when the response is
// still uncommitted, and then returns normally. When a request ID is already
// available, Recovery includes it in that fallback body. This keeps panic
// handling at the HTTP layer for ordinary request/response handlers.
//
// When propagate is true, Recovery switches to transport-abort propagation:
// it still logs the original panic value and stack trace, but it does not
// write a fallback response and instead re-panics with http.ErrAbortHandler.
// That mode is useful when a team wants net/http abort semantics, such as for
// streaming or proxy-style handlers, without also getting the standard
// library's own panic stack-trace logging at the server boundary.
func Recovery(logger *slog.Logger, propagate bool) Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := captureWriter(w, responseCaptureHooks{})
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic observed", slog.Any("panic", rec), slog.String("stack", string(debug.Stack())))
					if propagate {
						panic(http.ErrAbortHandler)
					}
					if !rw.Committed() {
						_ = writeDefaultJSONError(rw, http.StatusInternalServerError, "internal server error", recoveryFallbackRequestID(rw, r))
					}
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

func recoveryFallbackRequestID(w http.ResponseWriter, r *http.Request) string {
	if requestID := RequestIDFromContext(r.Context()); requestID != "" {
		return requestID
	}
	return w.Header().Get("X-Request-ID")
}

func newCorrelationValue() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}

	// Best-effort fallback for the extremely rare case where crypto/rand is
	// unavailable. This remains unique enough for request-scoped IDs without
	// degrading all the way to a bare monotonic counter.
	binary.BigEndian.PutUint64(buf[:8], uint64(time.Now().UnixNano()))
	seq := requestCounter.Add(1)
	pid := uint64(os.Getpid()) << 32
	binary.BigEndian.PutUint64(buf[8:], seq^pid)
	return hex.EncodeToString(buf[:])
}

// RequestIDFromContext returns the request ID inserted by RequestID.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

// CorrelationIDFromContext returns the correlation ID inserted by CorrelationID.
func CorrelationIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(correlationIDKey).(string)
	return v
}

func withMatchedRoute(r *http.Request) *http.Request {
	if _, ok := r.Context().Value(matchedRouteKey).(*matchedRoute); ok {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), matchedRouteKey, &matchedRoute{}))
}

func matchedRoutePath(r *http.Request) string {
	if matched, _ := r.Context().Value(matchedRouteKey).(*matchedRoute); matched != nil {
		return matched.path
	}
	return ""
}

func setMatchedRoutePath(r *http.Request, path string) {
	if matched, _ := r.Context().Value(matchedRouteKey).(*matchedRoute); matched != nil {
		matched.path = path
	}
}

func withSkipLogState(r *http.Request) *http.Request {
	if _, ok := r.Context().Value(skipLogKey).(*skipLogState); ok {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), skipLogKey, &skipLogState{}))
}

func markSkipAccessLog(r *http.Request) *http.Request {
	if state, _ := r.Context().Value(skipLogKey).(*skipLogState); state != nil {
		state.skip = true
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), skipLogKey, &skipLogState{skip: true}))
}

func skipAccessLogRequested(r *http.Request) bool {
	if state, _ := r.Context().Value(skipLogKey).(*skipLogState); state != nil {
		return state.skip
	}
	return false
}

func markSkipTelemetry(r *http.Request) *http.Request {
	if state, _ := r.Context().Value(skipTelemetryKey).(*skipTelemetryState); state != nil {
		state.skip = true
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), skipTelemetryKey, &skipTelemetryState{skip: true}))
}

func skipTelemetryRequested(r *http.Request) bool {
	if state, _ := r.Context().Value(skipTelemetryKey).(*skipTelemetryState); state != nil {
		return state.skip
	}
	return false
}

func withRequestOutcome(r *http.Request) *http.Request {
	if _, ok := r.Context().Value(requestOutcomeKey).(*requestOutcome); ok {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), requestOutcomeKey, &requestOutcome{}))
}

func requestOutcomeState(r *http.Request) *requestOutcome {
	outcome, _ := r.Context().Value(requestOutcomeKey).(*requestOutcome)
	return outcome
}

func markRequestTimedOut(r *http.Request) {
	if outcome := requestOutcomeState(r); outcome != nil {
		outcome.timedOut = true
	}
}

func markRequestCanceled(r *http.Request) {
	if outcome := requestOutcomeState(r); outcome != nil {
		outcome.canceled = true
	}
}

func markRequestAuthRejected(r *http.Request) {
	if outcome := requestOutcomeState(r); outcome != nil {
		outcome.authRejected = true
	}
}

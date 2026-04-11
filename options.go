package servekit

import (
	"log"
	"log/slog"
	"net/http"
	"time"
)

// Option mutates server configuration during New.
type Option func(*Server)

// WithAddr sets the TCP listen address (for example, ":8080").
func WithAddr(addr string) Option {
	return func(s *Server) {
		s.addr = addr
	}
}

// WithLogger sets the logger used by built-in middleware and server internals.
//
// When WithHTTPServerErrorLog is not supplied, Run also derives http.Server's
// ErrorLog from this logger's handler. That derived logger reuses the handler's
// output, formatting, and attributes, but the emitted record level comes from
// WithHTTPServerErrorLogLevel rather than from the handler's own threshold.
//
// A nil logger is ignored and leaves the current logger unchanged.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithHTTPServerErrorLog sets the stdlib logger used for http.Server.ErrorLog.
//
// This is an advanced override for users who want full control over the
// server's internal transport and accept-loop logging. When unset, Run derives
// ErrorLog from the server slog logger's handler and the configured
// WithHTTPServerErrorLogLevel value. When this option is set, it takes
// precedence and WithHTTPServerErrorLogLevel is ignored.
func WithHTTPServerErrorLog(logger *log.Logger) Option {
	return func(s *Server) {
		if logger != nil {
			s.httpServerErrorLog = logger
		}
	}
}

// WithHTTPServerErrorLogLevel sets the emitted slog level used when Run derives
// http.Server.ErrorLog from the server slog logger's handler.
//
// This option is ignored when WithHTTPServerErrorLog supplies an explicit
// stdlib logger. It controls the severity label attached to derived ErrorLog
// records. It does not change the slog handler's own enabled threshold.
func WithHTTPServerErrorLogLevel(level slog.Level) Option {
	return func(s *Server) {
		s.httpServerErrorLogLevel = level
	}
}

// WithMux replaces the underlying http.ServeMux used for route registration.
//
// A nil mux is ignored. This is an advanced escape hatch for integration with
// an existing mux. Most users should let Servekit manage its own mux.
func WithMux(mux *http.ServeMux) Option {
	return func(s *Server) {
		if mux != nil {
			s.mux = mux
		}
	}
}

// WithMiddleware appends global middleware to the server handler stack.
func WithMiddleware(mw ...Middleware) Option {
	return func(s *Server) {
		s.middlewares = append(s.middlewares, mw...)
	}
}

// WithCORSConfig opts into Servekit's built-in CORS middleware. CORS is
// disabled by default.
//
// AllowedOrigins is origin-based (scheme + host + port), not domain-based.
// Host or domain allowlisting is intentionally out of scope for Servekit and
// belongs at the ingress or reverse-proxy layer.
//
// Servekit validates the config when Handler constructs the middleware.
// Invalid AllowCredentials and AllowedOrigins combinations panic with a
// servekit-prefixed message.
func WithCORSConfig(cfg CORSConfig) Option {
	return func(s *Server) {
		s.corsConfig = &cfg
	}
}

// WithReadinessChecks appends checks evaluated by the built-in /readyz endpoint.
func WithReadinessChecks(checks ...ReadinessCheck) Option {
	return func(s *Server) {
		s.readinessChecks = append(s.readinessChecks, checks...)
	}
}

// WithResponseEncoder sets the default success response encoder for Handle.
//
// A nil encoder is ignored.
func WithResponseEncoder(encoder ResponseEncoder) Option {
	return func(s *Server) {
		if encoder != nil {
			s.responseEncoder = encoder
		}
	}
}

// WithErrorEncoder sets the default error encoder used by Handle.
//
// A nil encoder is ignored.
func WithErrorEncoder(encoder ErrorEncoder) Option {
	return func(s *Server) {
		if encoder != nil {
			s.errorResponse = encoder
		}
	}
}

// WithBuildInfo overrides the version, commit, and date fields served by the
// built-in /version endpoint.
func WithBuildInfo(version, commit, date string) Option {
	return func(s *Server) {
		s.buildInfo.Version = version
		s.buildInfo.Commit = commit
		s.buildInfo.Date = date
	}
}

// WithHealthHandler mounts a user-defined health endpoint at /healthz.
//
// Servekit does not impose built-in health semantics beyond /livez and
// /readyz. Use this hook when your service wants a broader or richer health
// endpoint without replacing the default operational routes.
func WithHealthHandler(handler http.Handler) Option {
	return func(s *Server) {
		if handler != nil {
			s.healthHandler = handler
		}
	}
}

// WithRecoveryEnabled enables or disables Servekit's panic recovery middleware.
//
// The default is true.
//
// When enabled, Servekit installs Recovery around the handler stack. With the
// default WithPanicPropagation(false), recovered requests do not re-panic:
// Recovery logs the original panic value and stack trace, writes a best-effort
// JSON 500 when the response is still uncommitted, and then returns normally.
//
// WithPanicPropagation(true) changes only that recovery behavior. In that
// mode Recovery still logs the original panic, but it does not attempt a
// fallback response and instead re-panics with http.ErrAbortHandler so
// net/http aborts the request.
//
// When enabled is false, Servekit does not install the outer Recovery
// middleware. Panics still escape to the surrounding net/http server or test
// harness, although inner observability middleware may briefly recover and
// re-panic so they can record logs, spans, or metrics.
func WithRecoveryEnabled(enabled bool) Option {
	return func(s *Server) {
		s.enableRecovery = enabled
	}
}

// WithPanicPropagation switches Recovery between contain-and-continue mode and
// transport-abort propagation mode.
//
// This option only has an effect when recovery middleware is enabled.
//
// The default is false. In that mode recovered requests do not re-panic:
// Recovery logs the original panic value and stack trace, writes a best-effort
// JSON 500 when the response is still uncommitted, and then returns normally.
//
// When enabled is true, Recovery switches to the mutually exclusive propagate
// mode. In that mode it still logs the original panic value and stack trace,
// but it never writes a fallback status code or body and instead re-panics
// with http.ErrAbortHandler. Teams typically choose this mode when they want
// connection-abort semantics from net/http, such as preserving streaming or
// proxy behavior, while still suppressing the standard library's own panic
// stack-trace logging at the server boundary.
func WithPanicPropagation(enabled bool) Option {
	return func(s *Server) {
		s.panicPropagation = enabled
	}
}

// WithAccessLogEnabled enables or disables request access logging middleware.
func WithAccessLogEnabled(enabled bool) Option {
	return func(s *Server) {
		s.enableAccessLog = enabled
	}
}

// WithRequestIDEnabled enables or disables request ID middleware.
func WithRequestIDEnabled(enabled bool) Option {
	return func(s *Server) {
		s.enableRequestID = enabled
	}
}

// WithCorrelationIDEnabled enables or disables correlation ID middleware.
func WithCorrelationIDEnabled(enabled bool) Option {
	return func(s *Server) {
		s.enableCorrelation = enabled
	}
}

// WithDefaultEndpointsEnabled enables or disables built-in /livez, /readyz,
// and /version endpoints, plus /healthz when WithHealthHandler is supplied.
func WithDefaultEndpointsEnabled(enabled bool) Option {
	return func(s *Server) {
		s.enableDefaultEndpoints = enabled
	}
}

// WithRequestBodyLimit sets the default maximum request body size in bytes
// for all endpoints. Individual endpoints can override this with
// WithBodyLimit. Set to -1 to disable the limit globally.
//
// The default is 4 MiB. This limit is enforced via http.MaxBytesReader,
// which causes reads beyond the limit to return *http.MaxBytesError, mapped
// to HTTP 413.
func WithRequestBodyLimit(n int64) Option {
	return func(s *Server) { s.requestBodyLimit = n }
}

// WithReadTimeout sets http.Server.ReadTimeout.
func WithReadTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.readTimeout = timeout
	}
}

// WithReadHeaderTimeout sets http.Server.ReadHeaderTimeout.
func WithReadHeaderTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.readHeaderTimeout = timeout
	}
}

// WithWriteTimeout sets http.Server.WriteTimeout.
//
// For streaming, SSE, reverse proxying, and other long-lived responses, you
// will often want to override the default and use zero or another value that
// matches the endpoint behavior.
func WithWriteTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.writeTimeout = timeout
	}
}

// WithIdleTimeout sets http.Server.IdleTimeout.
func WithIdleTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.idleTimeout = timeout
	}
}

// WithMaxHeaderBytes sets http.Server.MaxHeaderBytes when n is greater than zero.
func WithMaxHeaderBytes(n int) Option {
	return func(s *Server) {
		if n > 0 {
			s.maxHeaderBytes = n
		}
	}
}

// WithShutdownTimeout sets the timeout used for graceful shutdown.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(s *Server) {
		s.shutdownTimeout = timeout
	}
}

// WithShutdownDrainDelay waits after becoming unready before shutdown begins.
func WithShutdownDrainDelay(delay time.Duration) Option {
	return func(s *Server) {
		s.drainDelay = delay
	}
}

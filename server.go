package servekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jaredjakacky/servekit/version"
)

const (
	defaultAddr                    = ":8080"
	defaultReadTimeout             = 5 * time.Second
	defaultReadHeaderTimeout       = 2 * time.Second
	defaultWriteTimeout            = 10 * time.Second
	defaultIdleTimeout             = 60 * time.Second
	defaultMaxHeaderBytes          = 1 << 20
	defaultShutdownTimeout         = 15 * time.Second
	defaultRequestBodyLimit  int64 = 4 << 20 // 4 MiB
)

// Server bootstraps an HTTP service with production-oriented defaults.
//
// Server keeps net/http as the execution model while providing explicit hooks
// for middleware, probe checks, encoding, and lifecycle tuning via Option.
type Server struct {
	addr string

	logger *slog.Logger
	// httpServerErrorLog is an optional explicit override for http.Server.ErrorLog.
	// When nil, Run derives a stdlib *log.Logger from the slog handler below.
	httpServerErrorLog *log.Logger
	// httpServerErrorLogLevel is the fixed slog level assigned to records created
	// by the derived stdlib ErrorLog bridge. It does not come from the handler's
	// own filtering threshold.
	httpServerErrorLogLevel slog.Level
	responseEncoder         ResponseEncoder
	errorResponse           ErrorEncoder
	buildInfo               version.Info
	corsConfig              *CORSConfig
	healthHandler           http.Handler

	mux *http.ServeMux

	middlewares     []Middleware
	readinessChecks []ReadinessCheck

	readTimeout       time.Duration
	readHeaderTimeout time.Duration
	writeTimeout      time.Duration
	idleTimeout       time.Duration
	shutdownTimeout   time.Duration
	drainDelay        time.Duration
	requestBodyLimit  int64
	maxHeaderBytes    int

	enableRecovery         bool
	panicPropagation       bool
	enableAccessLog        bool
	enableRequestID        bool
	enableCorrelation      bool
	enableDefaultEndpoints bool
	enableOpenTelemetry    bool

	// observabilityOverrides stores only explicit user customizations. The
	// effective OTel config is derived later if observability middleware is enabled.
	observabilityOverrides observabilityConfig
	serverMetricsOnce      sync.Once
	serverMetrics          *otelServerMetrics
	handlerOnce            sync.Once
	builtHandler           http.Handler
	skipTelemetryPatterns  map[string]struct{}

	ready              atomic.Bool
	readySetExplicitly atomic.Bool
}

// New constructs a Server with defaults and applies opts in order.
//
// Defaults include JSON response/error encoders, middleware for panic logging,
// OpenTelemetry, request IDs, correlation IDs, and access logs, built-in
// /livez, /readyz, and /version endpoints, plus conservative http.Server
// timeout values.
//
// A newly constructed Server starts not ready. By default Run marks readiness
// true once serving starts, unless the application has already opted into
// explicit readiness control via SetReady. Applications that use Handler with
// an external http.Server lifecycle should call SetReady(true) only once they
// are actually ready to receive traffic.
func New(opts ...Option) *Server {
	s := &Server{
		addr:                    defaultAddr,
		logger:                  slog.Default(),
		httpServerErrorLogLevel: slog.LevelWarn,
		responseEncoder:         JSONResponse(),
		errorResponse:           JSONError(),
		buildInfo:               version.Get(),
		mux:                     http.NewServeMux(),
		readTimeout:             defaultReadTimeout,
		readHeaderTimeout:       defaultReadHeaderTimeout,
		writeTimeout:            defaultWriteTimeout,
		idleTimeout:             defaultIdleTimeout,
		shutdownTimeout:         defaultShutdownTimeout,
		requestBodyLimit:        defaultRequestBodyLimit,
		maxHeaderBytes:          defaultMaxHeaderBytes,
		drainDelay:              0,
		enableRecovery:          true,
		panicPropagation:        false,
		enableAccessLog:         true,
		enableRequestID:         true,
		enableCorrelation:       true,
		enableDefaultEndpoints:  true,
		enableOpenTelemetry:     true,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.enableDefaultEndpoints {
		s.registerDefaultEndpoints()
	}
	return s
}

// SetReady overrides the readiness state reported by the built-in /readyz route.
//
// Calling SetReady opts the server into explicit readiness control. After
// SetReady has been called, Run will not force readiness true during startup.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
	s.readySetExplicitly.Store(true)
}

// Ready reports the current readiness state exposed by /readyz.
func (s *Server) Ready() bool {
	return s.ready.Load()
}

// Handler builds the server's root http.Handler.
//
// It wraps the server mux with any enabled built-in middleware and any
// middleware added via WithMiddleware. Request flow is: CORS (when
// configured), panic logging (when enabled), OpenTelemetry (when enabled),
// request ID (when enabled), correlation ID (when enabled), access log (when
// enabled), custom middleware, then the mux.
func (s *Server) Handler() http.Handler {
	s.handlerOnce.Do(func() {
		var stack []Middleware
		if s.corsConfig != nil {
			stack = append(stack, newCORSMiddleware(*s.corsConfig))
		}
		if s.enableRecovery {
			stack = append(stack, Recovery(s.logger, s.panicPropagation))
		}
		if s.enableOpenTelemetry {
			stack = append(stack, s.observabilityMiddlewares()...)
		}
		if s.enableRequestID {
			stack = append(stack, RequestID())
		}
		if s.enableCorrelation {
			stack = append(stack, CorrelationID())
		}
		if s.enableAccessLog {
			stack = append(stack, AccessLog(s.logger))
		}
		stack = append(stack, s.middlewares...)
		s.builtHandler = Chain(s.mux, stack...)
	})
	return s.builtHandler
}

// Run starts serving and blocks until shutdown completes or serving fails.
//
// Run listens on WithAddr (default :8080), delegates request serving to
// net/http.Server, marks readiness true once serving starts unless readiness
// has already been set explicitly, and initiates graceful shutdown when ctx is
// canceled or SIGINT/SIGTERM is received. During shutdown readiness is set
// false, optional drain delay is applied, then http.Server.Shutdown runs with
// WithShutdownTimeout.
//
// Run marks the server ready immediately once the listener is accepting
// connections, before any application-level warmup. If your service requires
// explicit warmup before receiving traffic, call SetReady(false) after New()
// and SetReady(true) only once warmup is complete. Run will not override a
// readiness state that was set explicitly.
//
// The exact transport and protocol capabilities visible to handlers come from
// the underlying net/http server configuration used for the active run path.
// Servekit's own wrappers preserve optional ResponseWriter capabilities that
// are already present. They do not invent new ones.
//
// Run also configures http.Server.ErrorLog. By default Servekit adapts the
// server slog handler into the stdlib *log.Logger shape that net/http expects.
// Users can override that logger explicitly with WithHTTPServerErrorLog.
//
// Run returns wrapped listen/shutdown errors or the serve loop error.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()

	var connState func(net.Conn, http.ConnState)
	if s.enableOpenTelemetry {
		connState = s.serverMetricsCollector().connState
	}

	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadTimeout:       s.readTimeout,
		ReadHeaderTimeout: s.readHeaderTimeout,
		WriteTimeout:      s.writeTimeout,
		IdleTimeout:       s.idleTimeout,
		MaxHeaderBytes:    s.maxHeaderBytes,
		ErrorLog:          s.serverErrorLog(),
		ConnState:         connState,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	if !s.readySetExplicitly.Load() {
		s.ready.Store(true)
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-sigCtx.Done():
		s.ready.Store(false)
		if s.drainDelay > 0 {
			// Drain delay gives load balancers time to observe /readyz going false
			// before active listeners begin graceful shutdown.
			time.Sleep(s.drainDelay)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return <-serverErr
	case err := <-serverErr:
		return err
	}
}

func (s *Server) serverMetricsCollector() *otelServerMetrics {
	if !s.enableOpenTelemetry {
		return nil
	}
	s.serverMetricsOnce.Do(func() {
		obs := resolvedObservabilityConfig(s.observabilityOverrides)
		s.serverMetrics = newOTelServerMetrics(obs)
	})
	return s.serverMetrics
}

// serverErrorLog returns the stdlib logger passed to http.Server.ErrorLog.
//
// net/http expects a *log.Logger here, not a *slog.Logger. Servekit therefore
// either uses the user's explicit stdlib logger override or derives one from
// the configured slog handler. The derived bridge assigns a fixed slog level to
// every record it creates because the old log.Logger API does not carry
// structured severity metadata.
func (s *Server) serverErrorLog() *log.Logger {
	if s.httpServerErrorLog != nil {
		return s.httpServerErrorLog
	}
	return slog.NewLogLogger(s.logger.Handler(), s.httpServerErrorLogLevel)
}

func (s *Server) routeSkipsTelemetry(r *http.Request) bool {
	if len(s.skipTelemetryPatterns) == 0 {
		return false
	}
	_, pattern := s.mux.Handler(r)
	_, ok := s.skipTelemetryPatterns[pattern]
	return ok
}

// registerDefaultEndpoints installs the built-in operational routes.
//
// Servekit ships with fixed defaults for process liveness (/livez), traffic
// readiness (/readyz), and build diagnostics (/version). If a custom health
// handler is supplied, it is also mounted at /healthz. These routes are enabled
// by default via New unless WithDefaultEndpointsEnabled(false) is supplied.
func (s *Server) registerDefaultEndpoints() {
	s.HandleHTTP(http.MethodGet, "/livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStatusJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}), WithSkipAccessLog(), WithSkipTelemetry())

	s.HandleHTTP(http.MethodGet, "/readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Ready() {
			writeStatusJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
			return
		}
		for _, check := range s.readinessChecks {
			if err := check(r.Context()); err != nil {
				s.logger.Debug("readiness check failed", slog.Any("error", err))
				writeStatusJSON(w, http.StatusServiceUnavailable, map[string]any{
					"status": "not_ready",
					"reason": err.Error(),
				})
				return
			}
		}
		writeStatusJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	}), WithSkipAccessLog(), WithSkipTelemetry())

	if s.healthHandler != nil {
		s.HandleHTTP(http.MethodGet, "/healthz", s.healthHandler, WithSkipAccessLog(), WithSkipTelemetry())
	}

	s.HandleHTTP(http.MethodGet, "/version", s.buildInfo.Handler(), WithSkipAccessLog(), WithSkipTelemetry())
}

// writeStatusJSON writes a small JSON response with the provided status code.
func writeStatusJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

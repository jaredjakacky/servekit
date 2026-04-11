package servekit

import (
	"net/http"
	"strconv"
	"strings"
)

const (
	headerOrigin                     = "Origin"
	headerVary                       = "Vary"
	headerAccessControlAllowOrigin   = "Access-Control-Allow-Origin"
	headerAccessControlAllowMethods  = "Access-Control-Allow-Methods"
	headerAccessControlAllowHeaders  = "Access-Control-Allow-Headers"
	headerAccessControlAllowCreds    = "Access-Control-Allow-Credentials"
	headerAccessControlExposeHeaders = "Access-Control-Expose-Headers"
	headerAccessControlMaxAge        = "Access-Control-Max-Age"
	headerAccessControlRequestMethod = "Access-Control-Request-Method"
	headerAccessControlRequestHeader = "Access-Control-Request-Headers"
)

// CORSConfig configures Servekit's opt-in CORS middleware.
//
// AllowedOrigins is a list of exact origins in scheme://host[:port] form.
// When AllowCredentials is true, AllowedOrigins must be provided explicitly
// and must not contain "*", because credentialed CORS responses cannot allow
// all origins with a wildcard.
type CORSConfig struct {
	// AllowedOrigins is the exact origin allowlist in scheme://host[:port] form.
	// When empty and credentials are disabled, Servekit allows all origins.
	AllowedOrigins []string
	// AllowedMethods is the allowlist returned on successful preflight responses.
	// When empty, Servekit uses a conservative default set of common HTTP methods.
	AllowedMethods []string
	// AllowedHeaders is the allowlist for Access-Control-Request-Headers on
	// preflight requests. When empty, Servekit uses a small default set.
	AllowedHeaders []string
	// ExposedHeaders is the list of response headers browsers may expose to
	// calling JavaScript on successful cross-origin requests.
	ExposedHeaders []string
	// AllowCredentials enables Access-Control-Allow-Credentials: true on
	// successful CORS responses. When true, AllowedOrigins must be explicit and
	// must not contain "*".
	AllowCredentials bool
	// MaxAge controls Access-Control-Max-Age on successful preflight responses.
	// A value of 0 uses Servekit's default of 600 seconds. A positive value uses
	// the provided number of seconds. A negative value disables the header.
	MaxAge int
}

// corsPolicy is the normalized, request-ready form of CORSConfig.
//
// It precomputes lookup sets and header values once during middleware
// construction so request handling stays simple.
type corsPolicy struct {
	allowAllOrigins   bool
	allowCredentials  bool
	allowedOrigins    map[string]struct{}
	allowedMethodsCSV string
	allowedMethodsSet map[string]struct{}
	allowedHeadersCSV string
	allowedHeadersSet map[string]struct{}
	exposedHeaders    string
	maxAge            int
}

// newCORSMiddleware validates and normalizes CORSConfig once, then applies the
// resulting policy to each request.
func newCORSMiddleware(cfg CORSConfig) Middleware {
	policy := newCORSPolicy(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Requests without Origin are outside the browser CORS flow.
			origin := r.Header.Get(headerOrigin)
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			// A CORS preflight is an OPTIONS request that asks permission for a
			// later cross-origin request method.
			preflight := r.Method == http.MethodOptions && r.Header.Get(headerAccessControlRequestMethod) != ""
			if !policy.originAllowed(origin) {
				if preflight {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				// For non-preflight requests, CORS is enforced by the browser's
				// response handling rather than by refusing the HTTP request here.
				next.ServeHTTP(w, r)
				return
			}

			if preflight {
				// Successful preflights are answered entirely in middleware. The
				// application handler only sees the real request that follows.
				if !policy.preflightAllowed(r) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				policy.writeAllowOrigin(w.Header(), origin)
				if policy.allowCredentials {
					w.Header().Set(headerAccessControlAllowCreds, "true")
				}
				w.Header().Set(headerAccessControlAllowMethods, policy.allowedMethodsCSV)
				w.Header().Set(headerAccessControlAllowHeaders, policy.allowedHeadersCSV)
				if policy.maxAge > 0 {
					w.Header().Set(headerAccessControlMaxAge, strconv.Itoa(policy.maxAge))
				}
				appendVary(w.Header(), headerAccessControlRequestMethod, headerAccessControlRequestHeader)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// For non-preflight requests, attach the CORS response headers and
			// continue into the application handler.
			policy.writeAllowOrigin(w.Header(), origin)
			if policy.allowCredentials {
				w.Header().Set(headerAccessControlAllowCreds, "true")
			}
			if policy.exposedHeaders != "" {
				w.Header().Set(headerAccessControlExposeHeaders, policy.exposedHeaders)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// newCORSPolicy applies defaults, validates credential/origin constraints, and
// converts user config into lookup-friendly internal state.
func newCORSPolicy(cfg CORSConfig) corsPolicy {
	origins := trimNonEmpty(cfg.AllowedOrigins)
	if cfg.AllowCredentials && len(origins) == 0 {
		panic(`servekit: CORS AllowCredentials requires explicit AllowedOrigins`)
	}

	allowAllOrigins := len(origins) == 0 && !cfg.AllowCredentials
	if allowAllOrigins {
		origins = []string{"*"}
	} else {
		for _, origin := range origins {
			if origin != "*" {
				continue
			}
			if cfg.AllowCredentials {
				panic(`servekit: CORS AllowCredentials does not allow "*" in AllowedOrigins`)
			}
			allowAllOrigins = true
			break
		}
	}

	methods := normalizeMethods(cfg.AllowedMethods)
	if len(methods) == 0 {
		methods = []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		}
	}
	headers := normalizeHeaderNames(cfg.AllowedHeaders)
	if len(headers) == 0 {
		headers = []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"}
	}

	policy := corsPolicy{
		allowAllOrigins:   allowAllOrigins,
		allowCredentials:  cfg.AllowCredentials,
		allowedOrigins:    make(map[string]struct{}, len(origins)),
		allowedMethodsCSV: strings.Join(methods, ", "),
		allowedMethodsSet: make(map[string]struct{}, len(methods)),
		allowedHeadersCSV: strings.Join(headers, ", "),
		allowedHeadersSet: make(map[string]struct{}, len(headers)),
		exposedHeaders:    strings.Join(trimNonEmpty(cfg.ExposedHeaders), ", "),
	}
	if cfg.MaxAge == 0 {
		policy.maxAge = 600
	} else if cfg.MaxAge > 0 {
		policy.maxAge = cfg.MaxAge
	}
	for _, origin := range origins {
		policy.allowedOrigins[origin] = struct{}{}
	}
	for _, method := range methods {
		policy.allowedMethodsSet[method] = struct{}{}
	}
	for _, header := range headers {
		policy.allowedHeadersSet[header] = struct{}{}
	}
	return policy
}

// originAllowed reports whether the incoming Origin header matches the
// configured policy, including wildcard mode when enabled.
func (p corsPolicy) originAllowed(origin string) bool {
	if p.allowAllOrigins {
		return true
	}
	_, ok := p.allowedOrigins[origin]
	return ok
}

// preflightAllowed validates the browser's requested method and requested
// request headers for an incoming preflight request.
func (p corsPolicy) preflightAllowed(r *http.Request) bool {
	method := strings.ToUpper(r.Header.Get(headerAccessControlRequestMethod))
	if _, ok := p.allowedMethodsSet[method]; !ok {
		return false
	}
	for _, header := range parseHeaderList(r.Header.Get(headerAccessControlRequestHeader)) {
		if _, ok := p.allowedHeadersSet[header]; !ok {
			return false
		}
	}
	return true
}

// writeAllowOrigin emits Access-Control-Allow-Origin using either wildcard
// mode or the concrete request origin. When the response depends on the
// request's Origin value, it also appends Vary: Origin for cache correctness.
func (p corsPolicy) writeAllowOrigin(h http.Header, origin string) {
	if p.allowAllOrigins && !p.allowCredentials {
		h.Set(headerAccessControlAllowOrigin, "*")
		return
	}
	h.Set(headerAccessControlAllowOrigin, origin)
	appendVary(h, headerOrigin)
}

// trimNonEmpty removes surrounding whitespace and drops blank entries.
func trimNonEmpty(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

// normalizeMethods trims method names, drops blanks, and uppercases them so
// lookup and emitted header values use a consistent form.
func normalizeMethods(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		normalized = append(normalized, strings.ToUpper(value))
	}
	return normalized
}

// normalizeHeaderNames trims header names, drops blanks, and canonicalizes
// them so allowlist checks use the same shape as emitted header values.
func normalizeHeaderNames(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		normalized = append(normalized, http.CanonicalHeaderKey(value))
	}
	return normalized
}

// parseHeaderList parses a comma-separated HTTP header token list into
// canonicalized individual header names.
func parseHeaderList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	headers := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		headers = append(headers, http.CanonicalHeaderKey(part))
	}
	return headers
}

// appendVary adds Vary tokens without duplicating values that may already be
// present in repeated or comma-separated header form.
func appendVary(h http.Header, values ...string) {
	existing := make(map[string]struct{})
	for _, current := range h.Values(headerVary) {
		for _, part := range strings.Split(current, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			existing[strings.ToLower(part)] = struct{}{}
		}
	}
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := existing[key]; ok {
			continue
		}
		h.Add(headerVary, value)
		existing[key] = struct{}{}
	}
}

package servekit

import "net/http"

// Middleware wraps an http.Handler and returns a new handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares around h in declaration order.
//
// For Chain(h, a, b), requests flow as a -> b -> h.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middlewares[i] != nil {
			h = middlewares[i](h)
		}
	}
	return h
}

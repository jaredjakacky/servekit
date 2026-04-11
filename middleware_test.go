package servekit_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	servekit "github.com/jaredjakacky/servekit"
)

func TestChainAppliesMiddlewaresInDeclarationOrder(t *testing.T) {
	t.Parallel()

	var order []string
	a := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "a-before")
			w.Header().Add("X-Order", "a")
			next.ServeHTTP(w, r)
			order = append(order, "a-after")
		})
	}
	b := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "b-before")
			w.Header().Add("X-Order", "b")
			next.ServeHTTP(w, r)
			order = append(order, "b-after")
		})
	}

	h := servekit.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
		_, _ = io.WriteString(w, "ok")
	}), a, b)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
	if got := rec.Header().Values("X-Order"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("X-Order = %v, want [a b]", got)
	}

	wantOrder := []string{"a-before", "b-before", "handler", "b-after", "a-after"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order length = %d, want %d (%v)", len(order), len(wantOrder), order)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order[%d] = %q, want %q (full=%v)", i, order[i], wantOrder[i], order)
		}
	}
}

func TestChainSkipsNilMiddleware(t *testing.T) {
	t.Parallel()

	called := false
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.Header().Set("X-Middleware", "called")
			next.ServeHTTP(w, r)
		})
	}

	h := servekit.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), nil, mw, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("non-nil middleware was not called")
	}
	if got := rec.Header().Get("X-Middleware"); got != "called" {
		t.Fatalf("X-Middleware = %q, want %q", got, "called")
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestChainWithoutMiddlewareReturnsBaseHandlerBehavior(t *testing.T) {
	t.Parallel()

	h := servekit.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "base")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Body.String(); got != "base" {
		t.Fatalf("body = %q, want %q", got, "base")
	}
}

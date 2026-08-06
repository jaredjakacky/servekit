package servekit_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	servekit "github.com/jaredjakacky/servekit"
)

func TestHandleRegistersHandlerFuncRoute(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer()
	s.Handle(http.MethodGet, "/hello", func(r *http.Request) (any, error) {
		return map[string]string{"message": "hi"}, nil
	})

	rec := performRequest(t, s.Handler(), http.MethodGet, "/hello")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertHandlerJSONPathString(t, rec.Body.Bytes(), "data.message", "hi")
}

func TestHandleRoutesResponseEncodingFailureAfterCommittedSuccessStatus(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer()
	s.Handle(http.MethodGet, "/bad", func(r *http.Request) (any, error) {
		return make(chan int), nil
	})

	rec := performRequest(t, s.Handler(), http.MethodGet, "/bad")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertHandlerJSONPathString(t, rec.Body.Bytes(), "error", "internal server error")
}

func TestHandleHTTPRegistersRawHandlerRoute(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer()
	s.HandleHTTP(http.MethodGet, "/raw", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "raw")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "raw body")
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/raw")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := rec.Header().Get("X-Handler"); got != "raw" {
		t.Fatalf("X-Handler = %q, want %q", got, "raw")
	}
	if got := rec.Body.String(); got != "raw body" {
		t.Fatalf("body = %q, want %q", got, "raw body")
	}
}

func TestHandlePanicsOnInvalidRouteDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		path    string
		wantMsg string
	}{
		{name: "empty method", method: "", path: "/ok", wantMsg: "servekit: route method must not be empty"},
		{name: "empty path", method: http.MethodGet, path: "", wantMsg: "servekit: route path must not be empty"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newHandlerTestServer()
			defer func() {
				if recovered := recover(); recovered != tc.wantMsg {
					t.Fatalf("panic = %v, want %q", recovered, tc.wantMsg)
				}
			}()
			s.Handle(tc.method, tc.path, func(r *http.Request) (any, error) { return "ok", nil })
		})
	}
}

func TestWithEndpointMiddlewareWrapsEndpointOnly(t *testing.T) {
	t.Parallel()

	var order []string
	s := newHandlerTestServer(
		servekit.WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, "global-before")
				next.ServeHTTP(w, r)
				order = append(order, "global-after")
			})
		}),
	)
	s.HandleHTTP(http.MethodGet, "/mw", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
		_, _ = io.WriteString(w, "ok")
	}), servekit.WithEndpointMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "endpoint-before")
			w.Header().Set("X-Endpoint", "hit")
			next.ServeHTTP(w, r)
			order = append(order, "endpoint-after")
		})
	}), servekit.WithAuthGate(func(r *http.Request) error {
		order = append(order, "auth")
		return nil
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/mw")

	if got := rec.Header().Get("X-Endpoint"); got != "hit" {
		t.Fatalf("X-Endpoint = %q, want %q", got, "hit")
	}
	want := []string{"global-before", "auth", "endpoint-before", "handler", "endpoint-after", "global-after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q (full=%v)", i, order[i], want[i], order)
		}
	}
}

func TestWithEndpointResponseEncoderOverridesServerEncoderForHandle(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer(
		servekit.WithResponseEncoder(func(w http.ResponseWriter, r *http.Request, payload any) error {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "server")
			return nil
		}),
	)
	s.Handle(http.MethodGet, "/override", func(r *http.Request) (any, error) {
		return "payload", nil
	}, servekit.WithEndpointResponseEncoder(func(w http.ResponseWriter, r *http.Request, payload any) error {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "endpoint")
		return nil
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/override")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Body.String(); got != "endpoint" {
		t.Fatalf("body = %q, want %q", got, "endpoint")
	}
}

func TestWithAuthCheckRejectsUnauthorizedRequests(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer()
	called := false
	s.Handle(http.MethodGet, "/auth", func(r *http.Request) (any, error) {
		called = true
		return "ok", nil
	}, servekit.WithAuthCheck(func(r *http.Request) bool {
		return false
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/auth")

	if called {
		t.Fatal("handler was called for unauthorized request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertHandlerJSONPathString(t, rec.Body.Bytes(), "error", "unauthorized")
}

func TestWithAuthGateUsesReturnedError(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer()
	called := false
	s.Handle(http.MethodGet, "/gate", func(r *http.Request) (any, error) {
		called = true
		return "ok", nil
	}, servekit.WithAuthGate(func(r *http.Request) error {
		return servekit.Error(http.StatusForbidden, "forbidden", nil)
	}))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/gate")

	if called {
		t.Fatal("handler was called for auth gate failure")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	assertHandlerJSONPathString(t, rec.Body.Bytes(), "error", "forbidden")
}

func TestWithBodyLimitOverridesServerRequestBodyLimit(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer(servekit.WithRequestBodyLimit(2))
	s.Handle(http.MethodPost, "/body", func(r *http.Request) (any, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return string(body), nil
	}, servekit.WithBodyLimit(5))

	rec := performRequestWithBody(t, s.Handler(), http.MethodPost, "/body", "abcd")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertHandlerJSONPathString(t, rec.Body.Bytes(), "data", "abcd")
}

func TestWithEndpointTimeoutSetsRequestContextDeadline(t *testing.T) {
	t.Parallel()

	s := newHandlerTestServer()
	s.Handle(http.MethodGet, "/timeout", func(r *http.Request) (any, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			return nil, errors.New("missing deadline")
		}
		if time.Until(deadline) <= 0 {
			return nil, errors.New("deadline already expired")
		}
		return "timed", nil
	}, servekit.WithEndpointTimeout(50*time.Millisecond))

	rec := performRequest(t, s.Handler(), http.MethodGet, "/timeout")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertHandlerJSONPathString(t, rec.Body.Bytes(), "data", "timed")
}

func TestEndpointAuthRunsBeforeEndpointMiddleware(t *testing.T) {
	t.Parallel()

	registrations := []struct {
		name string
		raw  bool
	}{
		{name: "Handle"},
		{name: "HandleHTTP", raw: true},
	}
	authCases := []struct {
		name       string
		wantStatus int
		option     func(*[]string) servekit.EndpointOption
	}{
		{
			name:       "WithAuthCheck",
			wantStatus: http.StatusUnauthorized,
			option: func(order *[]string) servekit.EndpointOption {
				return servekit.WithAuthCheck(func(r *http.Request) bool {
					*order = append(*order, "auth")
					return false
				})
			},
		},
		{
			name:       "WithAuthGate",
			wantStatus: http.StatusForbidden,
			option: func(order *[]string) servekit.EndpointOption {
				return servekit.WithAuthGate(func(r *http.Request) error {
					*order = append(*order, "auth")
					return servekit.Error(http.StatusForbidden, "forbidden", nil)
				})
			},
		},
	}

	for _, registration := range registrations {
		registration := registration
		for _, authCase := range authCases {
			authCase := authCase
			t.Run(registration.name+"/"+authCase.name, func(t *testing.T) {
				t.Parallel()

				var order []string
				s := newHandlerTestServer()
				registerHandlerTestEndpoint(s, registration.raw, http.MethodGet, "/secure", func(r *http.Request) {
					order = append(order, "handler")
				},
					servekit.WithEndpointMiddleware(func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							order = append(order, "middleware")
							next.ServeHTTP(w, r)
						})
					}),
					authCase.option(&order),
				)

				rec := performRequest(t, s.Handler(), http.MethodGet, "/secure")

				if rec.Code != authCase.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, authCase.wantStatus)
				}
				if len(order) != 1 || order[0] != "auth" {
					t.Fatalf("order = %v, want [auth]", order)
				}
			})
		}
	}
}

func TestEndpointMiddlewareObservesEndpointDeadline(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  bool
	}{
		{name: "Handle"},
		{name: "HandleHTTP", raw: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var middlewareDeadline time.Time
			var afterDeadline time.Time
			var handlerDeadline time.Time
			s := newHandlerTestServer()
			registerHandlerTestEndpoint(s, tc.raw, http.MethodGet, "/deadline", func(r *http.Request) {
				handlerDeadline, _ = r.Context().Deadline()
			},
				servekit.WithEndpointMiddleware(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						middlewareDeadline, _ = r.Context().Deadline()
						next.ServeHTTP(w, r)
						afterDeadline, _ = r.Context().Deadline()
					})
				}),
				servekit.WithEndpointTimeout(time.Second),
			)

			rec := performRequest(t, s.Handler(), http.MethodGet, "/deadline")

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if middlewareDeadline.IsZero() {
				t.Fatal("endpoint middleware request has no deadline")
			}
			if time.Until(middlewareDeadline) <= 0 {
				t.Fatal("endpoint middleware deadline expired before request completed")
			}
			if !handlerDeadline.Equal(middlewareDeadline) {
				t.Fatalf("handler deadline = %v, want middleware deadline %v", handlerDeadline, middlewareDeadline)
			}
			if !afterDeadline.Equal(middlewareDeadline) {
				t.Fatalf("middleware deadline after next = %v, want %v", afterDeadline, middlewareDeadline)
			}
		})
	}
}

func TestEndpointMiddlewareIsSubjectToBodyLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  bool
	}{
		{name: "Handle"},
		{name: "HandleHTTP", raw: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotBody []byte
			var gotErr error
			handlerCalled := false
			s := newHandlerTestServer()
			registerHandlerTestEndpoint(s, tc.raw, http.MethodPost, "/body", func(r *http.Request) {
				handlerCalled = true
			},
				servekit.WithEndpointMiddleware(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						gotBody, gotErr = io.ReadAll(r.Body)
						w.WriteHeader(http.StatusRequestEntityTooLarge)
					})
				}),
				servekit.WithBodyLimit(3),
			)

			rec := performRequestWithBody(t, s.Handler(), http.MethodPost, "/body", "abcd")

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
			}
			if handlerCalled {
				t.Fatal("handler was called after endpoint middleware short-circuited")
			}
			if got := string(gotBody); got != "abc" {
				t.Fatalf("body read by middleware = %q, want %q", got, "abc")
			}
			var maxBytesErr *http.MaxBytesError
			if !errors.As(gotErr, &maxBytesErr) {
				t.Fatalf("middleware read error = %v, want *http.MaxBytesError", gotErr)
			}
			if maxBytesErr.Limit != 3 {
				t.Fatalf("MaxBytesError.Limit = %d, want 3", maxBytesErr.Limit)
			}
		})
	}
}

func registerHandlerTestEndpoint(s *servekit.Server, raw bool, method, path string, h func(*http.Request), opts ...servekit.EndpointOption) {
	if raw {
		s.HandleHTTP(method, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h(r)
			w.WriteHeader(http.StatusNoContent)
		}), opts...)
		return
	}
	s.Handle(method, path, func(r *http.Request) (any, error) {
		h(r)
		return nil, nil
	}, opts...)
}

func newHandlerTestServer(opts ...servekit.Option) *servekit.Server {
	base := []servekit.Option{
		servekit.WithDefaultEndpointsEnabled(false),
		servekit.WithOpenTelemetryEnabled(false),
		servekit.WithAccessLogEnabled(false),
		servekit.WithRequestIDEnabled(false),
		servekit.WithCorrelationIDEnabled(false),
	}
	base = append(base, opts...)
	return servekit.New(base...)
}

func assertHandlerJSONPathString(t *testing.T, body []byte, path, want string) {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}

	current := any(payload)
	for _, part := range strings.Split(path, ".") {
		next, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("path %q did not resolve to an object at %q", path, part)
		}
		current = next[part]
	}

	got, _ := current.(string)
	if got != want {
		t.Fatalf("JSON %q = %q, want %q", path, got, want)
	}
}

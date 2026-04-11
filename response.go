package servekit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// ResponseEncoder writes successful responses for Handle.
//
// Returning an error passes control to the server ErrorEncoder. Implementations
// should avoid committing a success status or body before returning an error
// whenever practical. Once a success response has been committed, the
// ErrorEncoder may no longer be able to replace it with an error response.
type ResponseEncoder func(http.ResponseWriter, *http.Request, any) error

// ErrorEncoder writes error responses for Handle.
//
// The returned error is ignored by Servekit, so implementations should treat
// best-effort response writes as terminal. If the response has already been
// committed by an earlier writer, an ErrorEncoder may not be able to change the
// status code or replace the response body.
type ErrorEncoder func(http.ResponseWriter, *http.Request, error) error

// JSONResponse returns the default success encoder for Handle.
//
// A nil payload writes HTTP 204 No Content with no body. A non-nil payload
// writes HTTP 200 with Content-Type application/json and body shape
// {"data": <payload>}. JSONResponse writes directly to the ResponseWriter using
// normal net/http response semantics. If JSON encoding fails after the success
// status has been committed, Handle will still call the server ErrorEncoder,
// but the error response may not be able to replace the already-committed
// success status.
func JSONResponse() ResponseEncoder {
	return func(w http.ResponseWriter, _ *http.Request, payload any) error {
		if payload == nil {
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return json.NewEncoder(w).Encode(map[string]any{"data": payload})
	}
}

// JSONError returns the default error encoder for Handle.
//
// JSONError maps HTTPError values to their StatusCode, maps context cancellation
// and deadline errors to HTTP 504, and otherwise returns HTTP 500. The payload
// shape is {"error": "..."} and includes request_id when one is present in the
// request context.
func JSONError() ErrorEncoder {
	return func(w http.ResponseWriter, r *http.Request, err error) error {
		status := statusFromError(err)
		return writeDefaultJSONError(w, status, clientErrorMessage(err, status), RequestIDFromContext(r.Context()))
	}
}

func clientErrorMessage(err error, status int) string {
	var httpErr HTTPError
	if errors.As(err, &httpErr) && httpErr.Message != "" {
		return httpErr.Message
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return "request body too large"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request canceled"
	}
	if text := http.StatusText(status); text != "" {
		return strings.ToLower(text)
	}
	return "error"
}

func writeDefaultJSONError(w http.ResponseWriter, status int, message, requestID string) error {
	body := map[string]any{"error": message}
	if requestID != "" {
		body["request_id"] = requestID
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}

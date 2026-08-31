package qdrant

import "fmt"

// ErrBackendUnavailable classifies a 5xx response or a connection-level
// failure (refused, timeout, DNS) — retryable by the caller.
var ErrBackendUnavailable = fmt.Errorf("qdrant: backend unavailable")

// ErrBackendRequest classifies a 4xx response or a malformed
// request/response — not retryable; the request itself is wrong.
var ErrBackendRequest = fmt.Errorf("qdrant: request error")

// Error wraps a Qdrant operation failure, classified via errors.Is
// against ErrBackendUnavailable/ErrBackendRequest.
type Error struct {
	Op    string
	Kind  error // ErrBackendUnavailable or ErrBackendRequest
	Cause error
}

func (e *Error) Error() string {
	return fmt.Sprintf("qdrant %s: %v", e.Op, e.Cause)
}

func (e *Error) Unwrap() []error {
	return []error{e.Kind, e.Cause}
}

// classify maps an HTTP status code to the appropriate error
// classification: 5xx is transient/retryable, everything else (4xx) is
// not.
func classify(statusCode int) error {
	if statusCode >= 500 {
		return ErrBackendUnavailable
	}
	return ErrBackendRequest
}

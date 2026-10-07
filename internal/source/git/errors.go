package git

import "fmt"

// ErrorKind classifies a CacheError as retryable (transient) or not
// (permanent), so a caller can decide whether to retry without inspecting
// error text. No caller outside this package reads it yet.
type ErrorKind string

const (
	ErrKindTransient ErrorKind = "transient"
	ErrKindPermanent ErrorKind = "permanent"
)

// CacheError wraps a git operation failure with a retryability
// classification.
type CacheError struct {
	Op    string
	Kind  ErrorKind
	Cause error
}

// Error formats the failed operation and its cause.
func (e *CacheError) Error() string {
	return fmt.Sprintf("git %s: %v", e.Op, e.Cause)
}

// Unwrap returns Cause.
func (e *CacheError) Unwrap() error {
	return e.Cause
}

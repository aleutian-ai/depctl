package git

import "fmt"

// ErrorKind classifies a CacheError as retryable (transient) or not
// (permanent), so callers such as the future job system can decide
// whether to retry without inspecting error text.
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

func (e *CacheError) Error() string {
	return fmt.Sprintf("git %s: %v", e.Op, e.Cause)
}

func (e *CacheError) Unwrap() error {
	return e.Cause
}

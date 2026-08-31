package resolver

import "fmt"

// ResolutionError wraps a Resolve failure so callers can distinguish
// transient causes (timeout) from permanent ones (missing tool, malformed
// lockfile) via errors.Is/errors.As on Cause.
type ResolutionError struct {
	Resolver string
	Root     string
	Cause    error
}

func (e *ResolutionError) Error() string {
	return fmt.Sprintf("resolver %s: resolve %s: %v", e.Resolver, e.Root, e.Cause)
}

func (e *ResolutionError) Unwrap() error {
	return e.Cause
}

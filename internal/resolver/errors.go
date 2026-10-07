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

// Error formats the resolver name, project root and cause.
func (e *ResolutionError) Error() string {
	return fmt.Sprintf("resolver %s: resolve %s: %v", e.Resolver, e.Root, e.Cause)
}

// Unwrap returns Cause.
func (e *ResolutionError) Unwrap() error {
	return e.Cause
}

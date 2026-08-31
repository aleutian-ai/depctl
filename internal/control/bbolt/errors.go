package bbolt

import "errors"

// ErrNotFound is returned by getters when the requested key does not
// exist, so callers can distinguish "missing" from a storage failure via
// errors.Is.
var ErrNotFound = errors.New("not found")

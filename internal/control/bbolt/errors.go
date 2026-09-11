package bbolt

import "errors"

// ErrNotFound is returned by getters when the requested key does not
// exist, so callers can distinguish "missing" from a storage failure via
// errors.Is.
var ErrNotFound = errors.New("not found")

// ErrLocked means another process (typically a long-running `ragctl
// serve`) holds control.db's exclusive file lock.
var ErrLocked = errors.New("control database is locked by another ragctl process (is `ragctl serve` running?)")

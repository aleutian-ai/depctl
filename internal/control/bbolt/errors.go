package bbolt

import "errors"

// ErrNotFound is returned by getters when the requested key does not
// exist, so callers can distinguish "missing" from a storage failure via
// errors.Is.
var ErrNotFound = errors.New("not found")

// ErrLocked means another process — under ADR-011, that's the single
// `depctl daemon run` process for as long as it's up — holds control.db's
// exclusive file lock.
var ErrLocked = errors.New("control database is locked by another depctl process (is `depctl daemon run` already running?)")

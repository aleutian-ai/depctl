// Package promote implements VAL-004: atomically promoting a validated
// candidate generation to ACTIVE, superseding whichever generation was
// previously active for the same dependency+backend.
package promote

import (
	"context"
	"errors"
	"fmt"

	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/lifecycle/validate"
)

// ErrValidationFailed is returned when any supplied validate.StructuralResult
// has Passed == false — Promote never opens a bbolt transaction in this
// case.
var ErrValidationFailed = errors.New("promote: candidate failed validation")

// ErrNotReady is returned when candidate isn't in state READY.
var ErrNotReady = errors.New("promote: candidate is not READY")

// Promote checks that every supplied validation result passed and that
// candidate is in state READY, then atomically promotes it to ACTIVE for
// backendName via bbolt.Store.PromoteGeneration — a single write
// transaction with no network/remote calls, since all acquisition/
// embedding/replication work already happened upstream (generation.Build,
// generation.Replicate) before Promote is ever called.
//
// backendName isn't part of the ticket's sketched signature — the
// design's active_generations/<dependency-id>/<backend-id> key format
// names a backend, but the signature shown took none, so one is added
// here; the active-generation pointer is inherently backend-scoped
// (VEC-003 tracks replica status per backend too), and Promote can't
// derive a backend name from a domain.Generation alone.
func Promote(ctx context.Context, store *bbolt.Store, candidate domain.Generation, backendName string, results ...validate.StructuralResult) error {
	for _, r := range results {
		if !r.Passed {
			return fmt.Errorf("%w: %s: %v", ErrValidationFailed, candidate.ID, r.Failures)
		}
	}
	if candidate.State != domain.GenReady {
		return fmt.Errorf("%w: %s is in state %s", ErrNotReady, candidate.ID, candidate.State)
	}
	return store.PromoteGeneration(ctx, candidate, backendName)
}

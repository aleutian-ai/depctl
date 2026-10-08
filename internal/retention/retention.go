// Package retention implements depctl's grace-period bookkeeping
// (RET-002) and GC eligibility planning (RET-003): keeping a dependency
// version's knowledge retained for a configurable window after a
// project stops referencing it, then computing which versions — and
// which failed, stuck or duplicate generations — are safe to delete.
package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// ControlStore is the narrow slice of *bbolt.Store this package needs —
// defined here (consumer-side) rather than imported as a concrete type,
// per this codebase's convention for cross-package interfaces.
type ControlStore interface {
	AddReference(ctx context.Context, r domain.VersionReference) error
	RemoveReference(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, projectID string) error
	ListReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.VersionReference, error)
	ListAllReferences(ctx context.Context) ([]domain.VersionReference, error)
	GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, backendName string) (domain.Generation, error)
	ListAllGenerations(ctx context.Context) ([]domain.Generation, error)
}

// DropReference removes projectID's reference to (ecosystem, pkg,
// version) and, if no reference of any reason remains for that version
// afterward, automatically adds a grace_period reference so retention
// doesn't make it GC-eligible immediately — RET-002's "keep it around
// for a while after the last project drops it" behavior. A version
// still held by another project, or by a "latest"/"manual_pin"
// reference, is left alone: some other reference already retains it, so
// starting a grace countdown would be meaningless (or, worse, would
// make RET-003 misjudge how long it's been since anything actually
// needed this version).
//
// The grace *duration* isn't a parameter here — only LastSeenAt gets
// stored, and the duration is applied later, once, at PlanGC's
// eligibility-check time (config can change between when a grace period
// starts and when GC actually runs; baking a duration into the stored
// reference would freeze it to whatever was configured at drop time).
func DropReference(ctx context.Context, store ControlStore, ecosystem domain.Ecosystem, pkg, version, projectID string) error {
	if err := store.RemoveReference(ctx, ecosystem, pkg, version, projectID); err != nil {
		return fmt.Errorf("retention: remove reference %s/%s/%s: %w", ecosystem, pkg, version, err)
	}

	remaining, err := store.ListReferences(ctx, ecosystem, pkg, version)
	if err != nil {
		return fmt.Errorf("retention: list references %s/%s/%s: %w", ecosystem, pkg, version, err)
	}
	if len(remaining) > 0 {
		return nil
	}

	now := time.Now()
	err = store.AddReference(ctx, domain.VersionReference{
		ProjectID:   domain.GracePeriodProjectID,
		Ecosystem:   ecosystem,
		Package:     pkg,
		Version:     version,
		Reason:      domain.ReferenceReasonGracePeriod,
		FirstSeenAt: now,
		LastSeenAt:  now,
	})
	if err != nil {
		return fmt.Errorf("retention: add grace_period reference %s/%s/%s: %w", ecosystem, pkg, version, err)
	}
	return nil
}

// EffectiveGracePeriod returns configured, or RET-002's documented
// 14-day default if configured is zero or negative — missing/zero grace
// period config must never be read as "no grace period," which would
// make every version GC-eligible on the very next cycle.
func EffectiveGracePeriod(configured time.Duration) time.Duration {
	if configured <= 0 {
		return 336 * time.Hour
	}
	return configured
}

// GraceExpiry returns when r's grace period ends — only meaningful for
// a ReferenceReasonGracePeriod reference.
func GraceExpiry(r domain.VersionReference, gracePeriod time.Duration) time.Time {
	return r.LastSeenAt.Add(gracePeriod)
}

package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// OrphanCandidate is one domain.Generation GC-001 has determined is safe
// to delete: never promoted (or no longer reachable as active), and
// either FAILED or stuck non-terminal past orphanAge.
type OrphanCandidate struct {
	GenerationID string
	Ecosystem    domain.Ecosystem
	Package      string
	Version      string
	State        domain.GenerationState
	Reason       string // "failed" or "stale_nonterminal"
}

// nonTerminalOrphanStates is the set of in-progress states GC-001 will
// flag once a generation has been stuck in one of them, untouched, for
// longer than orphanAge. GenDiscovered/GenPlanned are deliberately
// excluded — they precede any acquisition/write work, so there's
// nothing to leak yet.
var nonTerminalOrphanStates = map[domain.GenerationState]bool{
	domain.GenAcquiring:   true,
	domain.GenNormalizing: true,
	domain.GenIndexing:    true,
	domain.GenValidating:  true,
}

// EffectiveOrphanAge returns configured, or a 24h default if configured
// is zero or negative — mirrors EffectiveGracePeriod exactly: config
// loading performs no defaulting of its own (yaml.Unmarshal simply
// leaves an absent/old-config field at its zero value), so an
// old-config-file zero must never be read as "flag everything
// immediately."
func EffectiveOrphanAge(configured time.Duration) time.Duration {
	if configured <= 0 {
		return 24 * time.Hour
	}
	return configured
}

// PlanOrphanGC computes every generation eligible for orphan garbage
// collection: FAILED, or non-terminal (ACQUIRING/NORMALIZING/INDEXING/
// VALIDATING) and untouched for longer than orphanAge, and not
// currently promoted as anyone's active generation. Pure over store
// reads — no deletion happens here (GC-003).
func PlanOrphanGC(ctx context.Context, store ControlStore, backendName string, orphanAge time.Duration, now time.Time) ([]OrphanCandidate, error) {
	orphanAge = EffectiveOrphanAge(orphanAge)

	gens, err := store.ListAllGenerations(ctx)
	if err != nil {
		return nil, fmt.Errorf("retention: list all generations: %w", err)
	}

	var candidates []OrphanCandidate
	for _, g := range gens {
		reason := ""
		switch {
		case g.State == domain.GenFailed:
			reason = "failed"
		case nonTerminalOrphanStates[g.State] && now.Sub(g.UpdatedAt) > orphanAge:
			reason = "stale_nonterminal"
		default:
			continue
		}

		eco, pkg := g.Dependency.Dependency.Ecosystem, g.Dependency.Dependency.Name
		if active, err := store.GetActiveGeneration(ctx, eco, pkg, g.Dependency.Version, backendName); err == nil && active.ID == g.ID {
			// Defensive: state says FAILED/non-terminal but this generation
			// is still the active pointer — a state/pointer inconsistency,
			// never GC-eligible regardless.
			continue
		}

		candidates = append(candidates, OrphanCandidate{
			GenerationID: g.ID,
			Ecosystem:    eco,
			Package:      pkg,
			Version:      g.Dependency.Version,
			State:        g.State,
			Reason:       reason,
		})
	}
	return candidates, nil
}

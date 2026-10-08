package retention

import (
	"context"
	"fmt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// SupersededDuplicateCandidate is one SUPERSEDED generation whose own
// (ecosystem, package, version) exactly matches some other generation
// that is currently ACTIVE — POINT-004's check-then-create race
// signature: two generations built for the identical version, only one
// promoted, the other left behind as dead weight.
type SupersededDuplicateCandidate struct {
	GenerationID string
	Ecosystem    domain.Ecosystem
	Package      string
	Version      string
}

// tupleKey groups generations by the (ecosystem, package, version) they
// were built for.
type tupleKey struct {
	ecosystem domain.Ecosystem
	pkg       string
	version   string
}

// PlanSupersededDuplicateGC finds every generation eligible for this
// narrower cleanup, distinct from PlanGC's reference/grace-period path
// and from PlanOrphanGC's never-promoted path. It never selects a
// SUPERSEDED generation whose (ecosystem, package, version) tuple has no
// currently-ACTIVE generation: with nothing serving that version, it is
// left to PlanGC's reference/grace-period rules. Only a same-version
// duplicate is unconditionally safe: its content is, by
// construction, identical to whichever generation for that exact
// version *is* now ACTIVE, so deleting it changes nothing about what
// search can return — unlike PlanGC/PlanOrphanGC, this needs no grace
// period and no reference check, since the redundancy is provable the
// moment it exists, not just once nothing references it anymore.
func PlanSupersededDuplicateGC(ctx context.Context, store ControlStore) ([]SupersededDuplicateCandidate, error) {
	all, err := store.ListAllGenerations(ctx)
	if err != nil {
		return nil, fmt.Errorf("retention: list all generations: %w", err)
	}

	byTuple := map[tupleKey][]domain.Generation{}
	for _, g := range all {
		k := tupleKey{g.Dependency.Dependency.Ecosystem, g.Dependency.Dependency.Name, g.Dependency.Version}
		byTuple[k] = append(byTuple[k], g)
	}

	var candidates []SupersededDuplicateCandidate
	for k, gens := range byTuple {
		hasActive := false
		for _, g := range gens {
			if g.State == domain.GenActive {
				hasActive = true
				break
			}
		}
		if !hasActive {
			continue
		}
		for _, g := range gens {
			if g.State != domain.GenSuperseded {
				continue
			}
			candidates = append(candidates, SupersededDuplicateCandidate{
				GenerationID: g.ID,
				Ecosystem:    k.ecosystem,
				Package:      k.pkg,
				Version:      k.version,
			})
		}
	}
	return candidates, nil
}

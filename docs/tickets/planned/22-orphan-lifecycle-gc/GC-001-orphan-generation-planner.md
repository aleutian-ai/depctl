# GC-001: Orphan generation planner

**Epic:** Orphan Lifecycle GC
**Status:** planned
**Depends on:** none (reads existing bbolt state; adds one new read method)
**Estimated size:** medium

## Goal
A pure, read-only planner — `retention.PlanOrphanGC`, a sibling to the existing `retention.PlanGC` (`internal/retention/gc_planner.go`) — that finds every `domain.Generation` that is `FAILED`, or stuck in a non-terminal state (`ACQUIRING`/`NORMALIZING`/`INDEXING`/`VALIDATING`, per `internal/domain/lifecycle.go`'s `GenerationState` constants) past a configurable age, and is not currently anyone's active generation. No deletion happens here, matching `PlanGC`'s own "pure over store reads" design.

## Non-goals
- No deletion — that's GC-003. This ticket only computes the candidate list.
- No lease/heartbeat/"in-flight job" tracking. Nothing in this codebase implements such a concept today (`domain.Job`/`JobState` tracks background job execution, not generation-build progress, and there's no `Lease` type anywhere in `internal/`). Rather than inventing one, this ticket relies on `Generation.UpdatedAt`, which is already bumped on every real lifecycle transition (`PutGeneration` is called at each stage of `generation.Build`/`Replicate`, per `internal/control/bbolt/generations.go`) — a build that's still actively progressing keeps re-stamping `UpdatedAt`, so it never crosses the age threshold while genuinely in flight. A generation only becomes a candidate once nothing has touched it for the full `orphanAge` window.
- No inclusion of `DISCOVERED`/`PLANNED` states. Those precede any acquisition/write work (nothing to leak yet), and the source design note (`docs/scratch/ragctl_architecture_eval_next_steps-2.md` §11.1) scopes the non-terminal set to `ACQUIRING`/`NORMALIZING`/`INDEXING`/`VALIDATING` specifically — the states where a build has actually started writing Badger/vector-backend content. Revisit only if `DISCOVERED`/`PLANNED` rows are observed accumulating in practice.
- No change to `retention.PlanGC` or its `GCCandidate` type — this is a new, independent function and a new candidate type (`OrphanCandidate`), not a variant of the existing one; the eligibility logic and identity (generation ID, not dependency+version) are different enough that folding them into one function/type would blur two distinct GC classes.

## Simplicity constraints
- `PlanOrphanGC` takes the same shape of parameters as `PlanGC` (`ctx`, `store`, `backendName`, an age/duration, `now`) for consistency, and is equally pure — no writes, no side effects, testable against a fixture store exactly like `PlanGC` already is.
- One new read method is added to bbolt (`ListAllGenerations`) and to the package-local `ControlStore` interface, mirroring the existing `ListAllReferences` pattern exactly (full bucket scan — `internal/control/bbolt/generations.go`'s own doc comment already notes this "small keyspace" tradeoff is already made twice, for `ListAllReferences` and `PlanGC`'s candidate discovery; this is the third, same tradeoff).
- Reuses `store.GetActiveGeneration` (already used by `PlanGC`) as the defensive "not anyone's active generation" check, rather than trusting `GenerationState` alone — belt-and-suspenders against a state/pointer inconsistency, same reasoning `PlanGC` already applies.

## Design
Package: `internal/retention` (new file, `orphan_planner.go`, sibling to `gc_planner.go`).

```go
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
```

`ControlStore` (`internal/retention/retention.go`) gains one method:
```go
type ControlStore interface {
	AddReference(ctx context.Context, r domain.VersionReference) error
	RemoveReference(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, projectID string) error
	ListReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.VersionReference, error)
	ListAllReferences(ctx context.Context) ([]domain.VersionReference, error)
	GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, backendName string) (domain.Generation, error)
	ListAllGenerations(ctx context.Context) ([]domain.Generation, error) // NEW
}
```

`internal/control/bbolt/generations.go` gains the implementation, mirroring `ListAllReferences`:
```go
// ListAllGenerations returns every generation record in the bucket —
// GC-001's orphan planner needs a fleet-wide scan since an orphaned
// generation, by definition, was never promoted and so has no
// dependency+version index pointing at it the way ListGenerationsByDependencyVersion
// requires already knowing which (ecosystem, package, version) to ask for.
func (s *Store) ListAllGenerations(ctx context.Context) ([]domain.Generation, error) {
	var gens []domain.Generation
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(generationsBucket)).ForEach(func(k, v []byte) error {
			var g domain.Generation
			if err := json.Unmarshal(v, &g); err != nil {
				return fmt.Errorf("unmarshal generation %s: %w", k, err)
			}
			gens = append(gens, g)
			return nil
		})
	})
	return gens, err
}
```

`orphan_planner.go`:
```go
// nonTerminalOrphanStates is the set of in-progress states GC-001 will
// flag once a generation has been stuck in one of them, untouched, for
// longer than orphanAge. GenDiscovered/GenPlanned are deliberately
// excluded — see this ticket's non-goals.
var nonTerminalOrphanStates = map[domain.GenerationState]bool{
	domain.GenAcquiring:   true,
	domain.GenNormalizing: true,
	domain.GenIndexing:    true,
	domain.GenValidating:  true,
}

// EffectiveOrphanAge returns configured, or a 24h default if configured
// is zero or negative — mirrors retention.EffectiveGracePeriod exactly:
// config.Load performs no defaulting of its own (yaml.Unmarshal simply
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
		if active, err := store.GetActiveGeneration(ctx, eco, pkg, backendName); err == nil && active.ID == g.ID {
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
```

## Inputs / Outputs
- Input: `store ControlStore`, `backendName string`, `orphanAge time.Duration` (GC-002 wires this from `config.Retention.OrphanAge`), `now time.Time`.
- Output: `[]OrphanCandidate`, one per eligible generation, each with the deletion path's needed identity (`GenerationID`) plus enough dependency context for a human-readable report (GC-002).

## Failure behavior
- `ListAllGenerations` failure (store error) aborts planning with a wrapped error — same as `PlanGC`'s `ListAllReferences` failure handling; no partial candidate list is ever returned on a read error.
- A generation record that fails to unmarshal is a hard error from `ListAllGenerations` (matching `ListAllReferences`'s existing behavior for a corrupt reference record) — orphan planning does not silently skip corrupt records, since silently skipping is exactly the kind of drift this planner exists to catch elsewhere.
- `GetActiveGeneration` returning `ErrNotFound` (no active generation at all for that dependency+backend) is the expected, common case for a truly orphaned generation — not an error condition; only a real `GetActiveGeneration` success whose `ID` matches excludes a candidate.

## Tests
- A `FAILED` generation with no active generation for its dependency: included, `Reason: "failed"`.
- An `ACQUIRING` generation whose `UpdatedAt` is older than `orphanAge`: included, `Reason: "stale_nonterminal"`.
- An `ACQUIRING` generation whose `UpdatedAt` is within `orphanAge`: excluded (still plausibly in progress).
- A `FAILED` generation that is nonetheless still the active-generation pointer (inconsistent state, constructed deliberately in the fixture): excluded.
- A `READY`/`ACTIVE`/`SUPERSEDED`/`GC_ELIGIBLE`/`DELETED`/`DISCOVERED`/`PLANNED` generation: never included, regardless of age.
- Two generations for the same dependency+version, one `FAILED` and one currently `ACTIVE`: only the `FAILED` one is a candidate — confirms candidates are generation-ID-scoped, not dependency+version-scoped like `PlanGC`'s.
- `orphanAge` of zero/negative: `EffectiveOrphanAge` substitutes the 24h default, exactly mirroring `retention.EffectiveGracePeriod`'s existing behavior for `RetentionConfig.GracePeriod` — confirmed against the real `config.Load` (`internal/config/config.go`), which performs no defaulting of its own beyond what `yaml.Unmarshal` leaves in place, so this substitution has to happen planner-side, not config-load-side.

## Acceptance criteria
- [ ] `retention.PlanOrphanGC` returns every `FAILED` generation not currently active, and every non-terminal generation stale past `orphanAge` and not currently active.
- [ ] `retention.ControlStore` and `internal/control/bbolt.Store` both implement `ListAllGenerations`.
- [ ] No promoted (`ACTIVE`) or already-terminal-and-cleaned-up (`DELETED`) generation is ever returned.
- [ ] `go test ./internal/retention/...` and `./internal/control/bbolt/...` pass, including new `ListAllGenerations`/`PlanOrphanGC` coverage.

# GC-002: Orphan GC config + CLI

**Epic:** Orphan Lifecycle GC
**Status:** planned
**Depends on:** GC-001
**Estimated size:** small

## Goal
Wire GC-001's `retention.PlanOrphanGC` into `ragctl gc` behind an explicit, opt-in `--orphans` flag, with the age threshold configurable via a new `config.Retention.OrphanAge` field. `ragctl gc --orphans --dry-run` prints candidates without deleting anything; `ragctl gc --orphans` prints and deletes them (once GC-003's deletion path exists). Orphan cleanup is never implicit — running plain `ragctl gc` (no `--orphans`) is completely unaffected, still only the existing reference-based `retention.PlanGC`/`gc.Run` path.

## Non-goals
- No default-on behavior, ever, for v1 — matches the design doc's explicit safety note (§11.2: "Do not make orphan cleanup implicit until behavior is well tested").
- No merging of the two candidate lists (reference-based vs. orphan-based) into one combined report or one combined deletion run — `--orphans` selects the orphan path *instead of* the normal path for that invocation, keeping the two deletion semantics (and their very different safety justifications) visibly separate rather than conflated in one table.
- No change to the existing `--dry-run` flag's meaning for the non-`--orphans` path.
- GC-003 owns the actual orphan deletion call this ticket's non-dry-run branch invokes — this ticket's scope is the flag/config/reporting wiring and the dry-run path, which needs no deletion capability at all.

## Simplicity constraints
- One new config field (`OrphanAge time.Duration`), one new cobra flag (`--orphans`), reusing the existing `--dry-run` flag rather than adding a second one — `--orphans --dry-run` together is the full safety-first invocation the design doc's §11.2 shows verbatim.
- `runGC` branches once, early, on `orphans bool` — the two paths (existing reference-based, new orphan-based) stay as separate, readable blocks rather than being threaded through shared conditional logic that has to reason about both candidate shapes at once.

## Design
Package: `internal/config` (`config.go`), `internal/cli` (`gc.go`).

`config.go`'s `RetentionConfig` gains one field:
```go
type RetentionConfig struct {
	GracePeriod time.Duration `yaml:"grace_period"`
	KeepLatest  bool          `yaml:"keep_latest"`
	OrphanAge   time.Duration `yaml:"orphan_age"` // NEW
}
```
`Default()` gains the design doc's suggested default:
```go
Retention: RetentionConfig{
	GracePeriod: 336 * time.Hour, // 14 days
	KeepLatest:  true,
	OrphanAge:   24 * time.Hour, // NEW
},
```
`Validate()` gains the matching non-negative check, alongside the existing `GracePeriod` one:
```go
if c.Retention.OrphanAge < 0 {
	return errors.New("retention.orphan_age: must be a non-negative duration")
}
```

`gc.go`:
```go
func newGCCmd() *cobra.Command {
	var dryRun, orphans bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Garbage-collect unreferenced versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if orphans {
				return runOrphanGC(cmd, dryRun)
			}
			return runGC(cmd, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print GC candidates without deleting anything")
	cmd.Flags().BoolVar(&orphans, "orphans", false, "target FAILED/stuck generations instead of unreferenced versions (see GC-001)")
	return cmd
}

func runOrphanGC(cmd *cobra.Command, dryRun bool) error {
	ctx := context.Background()

	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}

	candidates, err := retention.PlanOrphanGC(ctx, store, cfg.Vector.Backend, cfg.Retention.OrphanAge, time.Now())
	if err != nil {
		return fmt.Errorf("plan orphan GC: %w", err)
	}

	out := cmd.OutOrStdout()
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing eligible for orphan garbage collection")
		return nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-8s %-45s %-15s %-8s %-14s %s\n", c.Ecosystem, c.Package, c.Version, c.State, c.Reason, c.GenerationID)
	}

	if dryRun {
		return nil
	}

	// GC-003: badgerStore, vector backend, and the orphan deletion call
	// (gc.RunOrphans or equivalent) are wired here — see GC-003's design.
	return runOrphanDeletion(cmd, store, cfg, candidates)
}
```
(`runOrphanDeletion` is GC-003's addition; this ticket's own acceptance criteria only require the dry-run branch to be real and complete — the non-dry-run branch may land as part of this ticket or GC-003 depending on implementation order, but the flag/report/config shape above is this ticket's full scope either way.)

## Inputs / Outputs
- Input: `ragctl gc --orphans [--dry-run]`; `config.yaml`'s `retention.orphan_age` (duration string, e.g. `"24h"`).
- Output: a report table (ecosystem, package, version, state, reason, generation ID) to stdout for every orphan candidate; with `--dry-run`, nothing is deleted; without it, GC-003's deletion runs and per-candidate OK/FAIL lines are printed, matching `runGC`'s existing report style.

## Failure behavior
- `cfg.Retention.OrphanAge` of zero (e.g. an old config file predating this field, unmarshaled with the zero value since `config.Load` performs no defaulting beyond plain `yaml.Unmarshal` — confirmed against the real `Load` in `internal/config/config.go`, which only reads, unmarshals, and validates, never merges against `Default()`) is handled by GC-001's `retention.EffectiveOrphanAge`, called inside `PlanOrphanGC` itself — the same place `PlanGC` already substitutes `EffectiveGracePeriod` for a zero-valued `GracePeriod`. `runOrphanGC` passes `cfg.Retention.OrphanAge` straight through without its own defaulting logic, exactly as `runGC` already does for `cfg.Retention.GracePeriod`.
- No candidates found: prints a clear message, exits 0, same as `runGC`'s existing "nothing eligible" case.
- A backend/store error during planning aborts with a non-zero exit and wrapped error, same convention as `runGC`.

## Tests
- `ragctl gc --orphans --dry-run` against a fixture bbolt store with one `FAILED` and one healthy `ACTIVE` generation: prints exactly the `FAILED` one, deletes nothing (fixture store unchanged after the run).
- `ragctl gc` (no `--orphans`) against the same fixture: behavior and output identical to before this ticket — orphan generations are invisible to the normal path.
- `ragctl gc --orphans` (no `--dry-run`) against a fixture with a real orphan candidate: candidate is deleted (once GC-003 lands) and reported as `OK`, matching `runGC`'s existing per-candidate report format.
- Config round-trip: a `config.yaml` with `retention.orphan_age: 48h` loads into `cfg.Retention.OrphanAge == 48*time.Hour`; an absent `orphan_age` key loads the default.
- `config.Validate()` rejects a negative `orphan_age`.

## Acceptance criteria
- [ ] `config.RetentionConfig.OrphanAge` exists, defaults to 24h, validated non-negative.
- [ ] `ragctl gc --orphans --dry-run` reports orphan candidates and deletes nothing.
- [ ] `ragctl gc --orphans` (without `--dry-run`) reports and deletes orphan candidates.
- [ ] `ragctl gc` with no `--orphans` flag is behaviorally unchanged from before this ticket.
- [ ] `--orphans` is documented in `ragctl gc --help` output (cobra flag description).

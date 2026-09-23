# GC-003: Generation-scoped backend deletion

**Epic:** Orphan Lifecycle GC
**Status:** done
**Depends on:** GC-001, GC-002
**Estimated size:** medium

## Goal
The actual deletion path for an `OrphanCandidate`: remove its data from the vector backend, Badger, and bbolt. Unlike the existing epic-16 GC path (`internal/lifecycle/gc.Run`), which is scoped by *dependency+version* (safe because a `GCCandidate` is, by construction, a version nothing references and nothing has promoted), an orphan generation has no such scope available — it was never promoted, so "delete everything for this dependency+version" would be actively unsafe: a different, healthy generation could legitimately share the same dependency+version (e.g. a retry that succeeded after an earlier attempt failed) and epic 16's `DeleteAllReferences`/dependency+version-scoped vector delete would wrongly take it out too. Orphan deletion must be scoped by **generation ID alone**.

## Non-goals
- No change to `backend.VectorBackend`'s interface, `backend.Filter`'s fields, or the Qdrant adapter's `Delete`/`filterFrom`. Investigation of the real code (see Design) shows `backend.Filter` already has a `Generation string` field, and it is already fully wired end-to-end: `internal/backend/qdrant/types.go`'s `filterFrom` already maps `f.Generation` to a `"generation"` payload-match condition, `internal/backend/backendtest/fake.go`'s test double already filters on `f.Generation` too, and `PointMetadata.Generation` is already stamped onto every point at replicate time (`generation.Replicate`, the same generation ID that would identify an orphan). No new `Filter.GenerationID` field or dedicated `DeleteGeneration` backend method is needed — the capability the design doc's §11.3 speculatively proposed already exists and just needs to be used for this purpose.
- No reuse of `gc.Run`'s `ListGenerationsByDependencyVersion` + `DeleteAllReferences` combination for orphans — that combination is exactly what's unsafe here (see Goal). Orphan deletion calls `DeleteGenerationRecord` and Badger's `DeleteGeneration` directly by ID, and never calls `DeleteAllReferences` at all (an orphan generation, having never been promoted, has no reference rows of its own to clean up in the first place — any reference rows for that dependency+version belong to whichever generation, if any, actually got promoted).
- No change to `internal/lifecycle/gc.Run`, `retention.PlanGC`, or any epic-16 type — this ticket adds a new, parallel function in the same package, `gc.RunOrphans`, rather than modifying the existing one.

## Simplicity constraints
- Reuses `gc.ControlStore`/`gc.DataStore`'s existing method set wherever possible (`DeleteGenerationRecord`, `DeleteGeneration` are both already generation-ID-scoped, no change needed) — only the *orchestration* (which methods get called, and with what filter) differs from `gc.Run`, not the store interfaces themselves.
- Job bookkeeping (claim/resume/idempotent-retry, matching `gc.Run`'s existing restartability) is kept, using the same `domain.Job`/`JobState` machinery — but keyed directly off the generation ID (already a unique, stable identifier) instead of `gc.Run`'s `JobID("gc", dep)` hash-of-dependency-version scheme, since an orphan candidate's identity *is* its generation ID.

## Design
Package: `internal/lifecycle/gc` (new file, `orphans.go`, sibling to `gc.go`).

```go
// OrphanResult reports what happened for one retention.OrphanCandidate.
type OrphanResult struct {
	Candidate retention.OrphanCandidate
	Succeeded bool
	Error     string
}

// orphanJobID derives a stable job ID directly from the generation ID —
// unlike JobID (gc.go), which hashes a dependency+version because a
// GCCandidate has no ID of its own, an OrphanCandidate's GenerationID
// already is a unique, stable identifier.
func orphanJobID(generationID string) string {
	return "job_orphan_" + generationID
}

// RunOrphans executes every candidate in plan: claim/resume its job,
// delete its vector-backend points (filtered by Generation alone — see
// this ticket's Non-goals for why dependency+version scoping, as gc.Run
// uses, would be unsafe here), then its Badger generation data, then its
// bbolt generation record — deliberately never touching reference
// records, since an orphan generation was never promoted and so never
// owned any. Same fixed deletion order and same idempotent-retry
// behavior as gc.Run.
func RunOrphans(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, plan []retention.OrphanCandidate) ([]OrphanResult, error) {
	results := make([]OrphanResult, 0, len(plan))
	for _, candidate := range plan {
		results = append(results, runOneOrphan(ctx, control, data, vb, ns, candidate))
	}
	return results, nil
}

func deleteOrphanCandidate(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.OrphanCandidate) error {
	// Step 1: vector points for this exact generation only — reuses the
	// already-wired backend.Filter.Generation field, no interface change.
	if err := vb.Delete(ctx, backend.DeleteRequest{
		Namespace: ns.Name,
		Filter:    &backend.Filter{Generation: candidate.GenerationID},
	}); err != nil {
		return fmt.Errorf("delete vector replica for generation %s: %w", candidate.GenerationID, err)
	}

	// Step 2: Badger content for this generation.
	if err := data.DeleteGeneration(ctx, candidate.GenerationID); err != nil {
		return fmt.Errorf("delete badger generation %s: %w", candidate.GenerationID, err)
	}

	// Step 3: bbolt generation record. No DeleteAllReferences call — an
	// orphan generation was never promoted, so it never owned reference
	// rows; any reference rows for this dependency+version belong to a
	// different, actually-promoted generation, if one exists.
	if err := control.DeleteGenerationRecord(ctx, candidate.GenerationID); err != nil {
		return fmt.Errorf("delete generation record %s: %w", candidate.GenerationID, err)
	}
	return nil
}
```
`runOneOrphan` mirrors `runOne` (`gc.go`) exactly — claim job as `JobRunning` via `orphanJobID`, call `deleteOrphanCandidate`, mark `JobSucceeded`/`JobFailed` — same restartability contract, just against `OrphanCandidate`/`OrphanResult` instead of `GCCandidate`/`Result`.

`internal/cli/gc.go`'s `runOrphanGC` (GC-002) calls this once GC-003 lands:
```go
badgerStore, err := openDataStore()
if err != nil {
	return fmt.Errorf("open data store: %w", err)
}
defer badgerStore.Close()

vb, err := buildVectorBackend(cfg)
if err != nil {
	return err
}
ns := backend.Namespace{Name: cfg.Vector.Collection}

results, err := gc.RunOrphans(ctx, store, badgerStore, vb, ns, candidates)
if err != nil {
	return fmt.Errorf("run orphan GC: %w", err)
}
// ...OK/FAIL report, same shape as runGC's existing results loop.
```

## Inputs / Outputs
- Input: `[]retention.OrphanCandidate` (from GC-001's `PlanOrphanGC`), the same `ControlStore`/`DataStore`/`backend.VectorBackend`/`backend.Namespace` `gc.Run` already takes.
- Output: `[]OrphanResult`, one per candidate, `Succeeded`/`Error` — same shape and reporting convention as `gc.Run`'s `[]Result`.

## Failure behavior
- Same restartability contract as `gc.Run`: each step is independently idempotent (deleting an already-gone vector filter match, Badger generation, or bbolt record is a no-op), so re-running `RunOrphans` after a partial failure resumes from wherever it stopped, verified the same way `gc.Run`'s `TestRunResumesAfterPartialFailure` verifies it.
- A candidate's deletion failure marks its job `JobFailed` with `LastError` and does not block the remaining candidates in the same `plan` — matching `gc.Run`'s existing non-blocking-per-candidate behavior exactly.
- Re-running `RunOrphans` against a candidate whose job is already `JobSucceeded` is a no-op that reports success without redoing work — same as `gc.Run`'s `TestRunOnAlreadySucceededJobIsANoOp`.

## Tests
- End-to-end (mirroring `gc.RunOrphans`' sibling `gc_test.go`'s coverage of `Run`): a `FAILED` generation's vector points, Badger content, and bbolt record are all gone after `RunOrphans`; a separate, healthy `ACTIVE` generation for the *same dependency+version* is completely untouched — this is the specific case that would break if `RunOrphans` reused `gc.Run`'s dependency+version-scoped deletion instead of generation-ID scoping.
- `backend.Filter{Generation: id}` alone (no `Ecosystem`/`Dependency`/`Version`) correctly selects only that generation's points against both the real Qdrant adapter and `backendtest.Backend` (already confirmed structurally — `filterFrom` and the fake's filter-matching both already branch on `f.Generation` independently of the other fields — this ticket's test only needs to confirm the *call site* passes `Generation` and no other field, not that the backend implements it, since that plumbing already exists and is already covered by existing backend tests).
- Idempotency/resume: simulate a Badger-delete failure, confirm the bbolt record is untouched (same ordering discipline as `gc.Run`'s `TestRunResumesAfterPartialFailure`), then re-run and confirm completion.
- No `DeleteAllReferences` call happens anywhere in the orphan path — assert via a fake `ControlStore` that records whether it was called, expecting `false`.
- `retention.OrphanCandidate`'s three-store deletion runs in the fixed order (vector, then Badger, then bbolt) — same ordering assertion style as `gc.go`'s own doc comment describes for `Run`.

## Acceptance criteria
- [x] `gc.RunOrphans` deletes an orphan candidate's vector points (by generation ID only), Badger content, and bbolt record.
- [x] A healthy generation sharing the same dependency+version as an orphan candidate is never touched.
- [x] No new `backend.Filter` field or `VectorBackend` method was added — `Filter.Generation` (already existing) is reused as-is.
- [x] Orphan deletion never calls `DeleteAllReferences`.
- [x] Deletion is restartable/idempotent: a partial failure followed by a re-run completes cleanly, matching `gc.Run`'s existing guarantee.
- [x] `ragctl gc --orphans` (GC-002, without `--dry-run`) actually deletes candidates end-to-end.

## Post-implementation note
Implemented exactly as specced — the design section's code (`RunOrphans`, `deleteOrphanCandidate`, `orphanJobID`) was correct as written. `internal/lifecycle/gc/orphans.go` + `orphans_test.go` (4 tests, mirroring `gc_test.go`'s coverage shape): the key test, `TestRunOrphansDeletesGenerationScopedDataOnly`, constructs exactly the scenario this ticket's Goal describes — two generations sharing one dependency+version, one orphaned and one active — and confirms via direct vector-backend `Query` (filtered by `Generation`) that only the orphan's point is gone and the healthy one's is untouched, the concrete proof that generation-ID scoping (not dependency+version scoping) is what actually happened, not just what the code says it does. All pass on first run.

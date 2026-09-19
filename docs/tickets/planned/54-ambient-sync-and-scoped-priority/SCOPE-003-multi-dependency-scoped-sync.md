# SCOPE-003: Multi-dependency scoped sync

**Epic:** Ambient Sync and Scoped Priority
**Status:** planned
**Depends on:** none
**Estimated size:** medium

## Goal
Let a caller ask for an exact *set* of dependencies to be prioritized/synced, precisely — not one dependency at a time, and not by accident of two single-dependency requests colliding during coalescing. This is the foundation SCOPE-004's `prioritize_file` tool needs; without it, asking about a file's several imports can't be expressed as one precise, bounded request.

## Non-goals
- Not a general-purpose query/filter language — a plain set of exact dependency names, nothing more expressive.
- No change to the *meaning* of an empty/omitted set — still "everything," exactly as today's empty string does.

## Design

### The bug this fixes
`internal/daemon/scheduler.go`'s `mergeOptions`:
```go
if pending.Dependency == next.Dependency {
    merged.Dependency = pending.Dependency
}
```
Two *different* single-dependency requests for the same project, arriving close enough together to coalesce, produce a merged `Dependency` of `""` — which `RunSync`'s filter (`if dependency != "" && action.Dependency.Dependency.Name != dependency { continue }`) reads as "no filter, sync everything." Safe (never silently drops a dependency the caller asked about) but imprecise — asking about 4 specific imports could accidentally trigger a full 569-dependency sync if two of those 4 requests happen to land in the same coalescing window.

### The fix
1. `SyncOptions.Dependency string` → `SyncOptions.Dependencies []string` (`internal/daemon/scheduler.go`). An empty/nil slice means "everything," matching today's empty-string semantics exactly.
2. `mergeOptions` unions the two sets instead of requiring exact equality — two different single-dependency requests now correctly merge into "these two specifically," not "everything."
3. `RunSync`'s filter (`internal/cli/sync.go`) checks set membership instead of string equality.
4. `api.SyncRequest`/`api.SyncOptions` (wire types) gain the same shape; the HTTP client (`internal/daemon/client/client.go`) and `ragctl sync --dependency` CLI flag stay backward compatible — a single `--dependency` value becomes a one-element set at the CLI boundary, not a breaking change to the flag itself.
5. `SyncPriority.Bump`/`Drain` already handle multiple names correctly (it's already a slice-based FIFO) — no change needed there, confirmed by COORD-003's own `TestWorkerPoolHonorsPriorityBump`.

## Inputs / Outputs
- Input: a set of dependency names (or empty, for everything).
- Output: only actions matching that set are processed (or everything, if empty) — precisely, regardless of how requests happen to coalesce.

## Failure behavior
- A name in the set that doesn't match any resolved dependency is silently a no-op for that name (matches today's single-dependency behavior: an unknown `--dependency` value produces zero matching actions, not an error) — not changed by this ticket.

## Tests
- Two concurrent single-dependency requests for *different* names, for the same project, correctly coalesce into "these two, exactly" — not "everything." This is the direct regression test for the bug above; construct it by deliberately timing two `Scheduler.Request` calls to land in the same coalescing window (matching `TestSchedulerMergesFollowUpOptions`'s existing timing approach) and asserting the resulting sync only touched the two named dependencies.
- An empty set still means everything (no regression against today's default `ragctl sync` behavior).
- The CLI's single `--dependency` flag still works unchanged end-to-end.

## Acceptance criteria
- [ ] `mergeOptions` unions dependency sets instead of collapsing to "everything" on any mismatch.
- [ ] A direct regression test proves two colliding single-dependency requests no longer accidentally trigger a full sync.
- [ ] `ragctl sync --dependency` and the existing single-dependency JIT path (`search_dependency_docs`) are both unaffected by the internal shape change — verified by re-running COORD-002's own cross-project regression tests unmodified.

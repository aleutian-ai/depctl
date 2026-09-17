# EVENT-001: Persist a dependency-version-change record

**Epic:** Dependency Change Events
**Status:** planned
**Depends on:** existing `internal/planner.Plan`, `internal/control/bbolt`
**Estimated size:** small

## Goal
Whenever `planner.Plan` detects that a project's resolved version for a dependency has changed from what was previously recorded (the same diff that already triggers a `SYNC_VERSION` action), persist a small, standalone `DependencyChangeEvent` — old version, new version, when it was detected, which project — independent of the generation/knowledge lifecycle. This is infrastructure for a future upgrade-analysis feature, not that feature itself.

## Non-goals
- No summarization, no "what changed between these versions" content generation — that's a future consumer's job, reading this event log, not built here.
- No change to `planner.Plan`'s existing return shape (`[]Action`) or to `RunSync`'s control flow — this is an additive side-effect at the same detection point, not a redesign.
- No retention/expiry policy for these events in this ticket — start unbounded, matching how other append-only control-plane records in this repo already behave; revisit only if it becomes a real storage concern.

## Simplicity constraints
- One new bbolt bucket (`control/bbolt`), one new domain type, one new write call at the exact point `planner.Plan` (or its caller in `RunSync`) already detects the version diff — no new package.

## Design
`internal/domain`:
```go
// DependencyChangeEvent records one detected version transition for a
// project's dependency — a durable fact, independent of whether the
// resulting sync ever completes or fails.
type DependencyChangeEvent struct {
    ProjectID   string
    Dependency  Dependency
    OldVersion  string // empty for a first-ever resolution, not a "change"
    NewVersion  string
    DetectedAt  time.Time
}
```

`internal/control/bbolt`: `PutDependencyChangeEvent`, `ListDependencyChangeEvents(projectID string, dependency string)` (bucket keyed by `projectID/dependency/ULID`, matching existing bucket-key conventions elsewhere in this store).

Wiring: at the exact point in `planner.Plan` (or `RunSync`, whichever already has both the old `VersionReference` and the new resolved version in hand) where a `SYNC_VERSION` action is appended because the version actually changed (not a first-ever resolution), also call `PutDependencyChangeEvent`. A first-ever resolution (no prior `VersionReference`) is not a "change" and produces no event.

## Inputs / Outputs
- Input: the existing old-vs-new version diff `planner.Plan` already computes.
- Output: one `DependencyChangeEvent` per detected real version change, persisted in bbolt, independent of whether the resulting sync succeeds, fails, or is still running.

## Failure behavior
- The event write is best-effort relative to the sync itself — a failure to persist the event must never block or fail the actual `SYNC_VERSION` action; log and continue.

## Tests
- `planner.Plan` detecting a real version change (v1.71.0 → v1.75.0) produces exactly one `DependencyChangeEvent`.
- A first-ever resolution (no prior `VersionReference`) produces zero events.
- `ListDependencyChangeEvents` returns events in detection order for a given project/dependency.

## Acceptance criteria
- [ ] A real version change persists a `DependencyChangeEvent` with correct old/new versions and timestamp.
- [ ] A first-ever resolution produces no event.
- [ ] Event persistence failure never blocks the sync it's associated with.

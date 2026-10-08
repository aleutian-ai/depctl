# OPS-007: Git mirror lock defeated by a fresh `Cache` per sync call

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** small

## Goal
Found live during epic 49's STRESS-007 (concurrent cross-project sync against a real daemon): two real, genuinely concurrent `depctl sync` requests for two different projects, each depending on a different version of the same underlying git repository (`cloud.google.com/go/compute/metadata` at two different tags, both living in the `github.com/googleapis/google-cloud-go` monorepo), raced on that repository's shared git mirror directory. One sync failed with a real, reproducible error:

```
git clone --mirror https://github.com/googleapis/google-cloud-go: Cloning into bare repository '.../git/github.com/googleapis/google-cloud-go.git'...
fatal: cannot copy '.../git-core/templates/hooks/commit-msg.sample' to '.../git/github.com/googleapis/google-cloud-go.git/hooks/commit-msg.sample': File exists
```

`internal/source/git/cache.go`'s `EnsureMirror` already has an in-process `mirrorLocks map[string]*sync.Mutex` keyed by repository path, guarding exactly this scenario — its own doc comment names it directly ("two concurrent first-time acquisitions of the same repository... can't both run `git clone --mirror` into the same target directory at once"). But `internal/cli/sync.go`'s `RunSync` called `buildGitCache(cfg)` fresh, inside its own per-call lazily-built pipeline, on **every** `RunSync` invocation — so two concurrent syncs, even two handled by the same long-lived daemon process, each got their own `git.Cache` with its own empty `mirrorLocks` map. The lock existed but never actually shared state across the two calls that needed to share it, making it a no-op for exactly the case it was built for.

This became reachable in practice once epic 53 (`BuildCoordinator`) removed cross-project sync serialization — before that, only one `RunSync` call could be in flight system-wide at a time, so the per-call `Cache` was never actually concurrent with another one.

## Non-goals
- No change to the mirror-locking logic itself (`EnsureMirror`, `mirrorLocks`) — it's correct; the bug was in its lifetime, not its design.
- No change to `git.Cache`'s external-mirror/checkout-seed search tiers (GIT-006/GIT-007) — unaffected.

## Simplicity constraints
- Reuse the existing `engine` struct's established pattern for daemon-wide shared state (`syncSem`, sized once at construction) rather than inventing a new sharing mechanism.

## Design
1. `internal/cli/daemon.go`: `engine` gains a `gitCache *git.Cache` field, built once in `newEngine` (now returning an error, since `buildGitCache` can fail) and shared for the engine's — and therefore the daemon's — whole lifetime, exactly like `syncSem`.
2. `RunSync` (`internal/cli/sync.go`) takes `gitCache *git.Cache` as an explicit parameter instead of building its own; `engine.Sync` passes `e.gitCache`.
3. All three existing `RunSync` test call sites updated to pass `nil` (none of them exercise real git acquisition).

## Inputs / Outputs
- Input: two real, concurrent `depctl sync` requests needing the same underlying git repository at different tags.
- Output: both complete without a mirror-creation race, using the daemon's one shared `git.Cache` and its one shared `mirrorLocks` map.

## Failure behavior
- A missed race under this same scenario, or a deadlock from the now-shared lock, is this ticket's finding.

## Tests
- `go build ./...`, `go vet ./...`, and the full `internal/cli` suite (unchanged behavior for every existing test — `gitCache` was always nil-equivalent in tests, still is).
- Manual/live exercise via STRESS-007; results recorded there and summarized here.

## Acceptance criteria
- [x] `git.Cache` is constructed once per daemon lifetime, not once per `RunSync` call.
- [x] `RunSync`'s three existing test call sites still compile and pass, unchanged in behavior.
- [x] Full `go build`/`go vet`/`go test ./...` clean after the change.

## Post-implementation note (2026-09-29)

Fixed exactly as scoped. `newEngine` now returns `(*engine, error)` (its one call site, `runDaemonRun`, updated accordingly) and builds `gitCache` once via the existing `buildGitCache(cfg)` helper — unchanged itself, just called once instead of per-`RunSync`. `RunSync` gained a `gitCache *git.Cache` parameter, threaded from `e.gitCache` in `engine.Sync`; `getPipeline`'s own `buildGitCache(cfg)` call was removed. `internal/cli/sync_batch_timeout_test.go`, `sync_noop_progress_test.go`, and `sync_test.go` each got a trailing `nil` appended to their existing `RunSync(...)` calls — no other change needed, since none of them exercise real git acquisition.

Full `go build ./...`, `go vet ./...`, and `go test ./...` all pass clean after the change. Live re-verification of the actual race this fixes is folded into STRESS-007's own post-implementation note (its concurrent-sync scenario is the real reproduction case this ticket exists to fix) rather than repeated here.
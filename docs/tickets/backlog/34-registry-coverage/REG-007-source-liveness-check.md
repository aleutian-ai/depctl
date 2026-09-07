# REG-007: Source liveness check

**Epic:** Registry Coverage
**Status:** planned
**Depends on:** REG-002 (registry loader), OPS-002 (`ragctl doctor`), DESC-001 (`ragctl describe`)
**Estimated size:** small

## Goal
Detect that a registered source's URL is no longer reachable (repo moved/deleted, docs site 404s) before a sync attempt fails on it, and surface that in `doctor`/`describe` instead of only discovering it mid-sync.

## Non-goals
- No automatic remediation (removing/fixing a dead manifest entry) — diagnosis only, matching OPS-002's own non-goal on auto-repair.
- No scheduled/background liveness polling — a local-first CLI has no daemon to run it; liveness is checked on demand (`doctor`/`describe --check-liveness`), not continuously.
- No content-change detection (that's DATA-004's descriptor-comparison idea, for a different domain — raw datasets, not registry sources).

## Simplicity constraints
- A liveness check is cheap and read-only: `git ls-remote <url>` for git sources (no clone), a `HEAD` request for website sources. No new acquisition path, no full sync.
- Runs only when explicitly requested (a flag), never implicitly on every `describe`/`doctor` invocation — network calls should be opt-in for commands that are otherwise fully offline.

## Design
Package: `internal/registry` (small addition) or a sibling `internal/registry/liveness`.

```go
type LivenessResult struct {
    SourceID string
    Reachable bool
    CheckedAt time.Time
    Error string // populated when Reachable is false
}

func CheckLiveness(ctx context.Context, source registry.Source) LivenessResult
```

Wire into:
- `ragctl doctor --check-registry-liveness` (opt-in flag, since OPS-002's fixed check list is otherwise fully local/offline).
- `ragctl describe --check-liveness` (opt-in, annotates each `SourceEntry` with reachability).

## Inputs / Outputs
- Input: one `registry.Source`.
- Output: `LivenessResult` — reachable or not, with the underlying error preserved for a human to act on.

## Failure behavior
- Network unreachable entirely (offline machine): every check reports `Reachable: false` with a clear "network unreachable" distinguishing error, not conflated with "source moved/deleted."

## Tests
- A `git` source pointing at a real local test repo reports reachable.
- A `git` source pointing at a nonexistent path/URL reports unreachable with the underlying git error preserved.
- A `website` source returning non-2xx/3xx reports unreachable.
- Liveness checks never run unless explicitly requested (verified via a network-blocking transport in the unit test, same pattern as MCP-004's offline test).

## Acceptance criteria
- [x] `git`/`website` source liveness checks implemented.
- [ ] `doctor --check-registry-liveness` and `describe --check-liveness` both wire it in, both opt-in.
- [x] No liveness network call happens without the explicit flag.

## Post-implementation note
`CheckLiveness`/`LivenessResult` landed in `internal/registry` (not a separate `internal/registry/liveness` package — small enough to keep with the type it checks). `doctor --check-registry-liveness` is not wired in because `ragctl doctor` itself doesn't exist yet (epic 18, still a stub) — out of scope to build here. `describe --check-liveness` is wired in as DESC-ADV-001, landed separately right after this ticket.


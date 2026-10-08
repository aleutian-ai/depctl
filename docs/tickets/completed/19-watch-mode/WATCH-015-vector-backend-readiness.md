# WATCH-015: Vector backend (Qdrant) readiness

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-014 (embedding readiness — this ticket mirrors its pattern exactly, for the other half of the pipeline)
**Estimated size:** medium

## Goal
Live-verified gap (found running the real MCP-first scenario end to end after WATCH-014 shipped): a fresh install with Ollama/the embedding model both ready still fails every `sync_project` action with a raw error —
```
FAIL  github.com/spf13/cobra v1.10.2: replicate: generation: replication failed:
ensure namespace depctl: qdrant EnsureNamespace:
dial tcp 127.0.0.1:6333: connect: connection refused
```
— because nothing checks Qdrant's reachability before `generation.Replicate` tries to use it. Give the vector backend the exact same background-readiness treatment WATCH-014 gave the embedding backend: checked once at daemon startup, off any client's request path, surfaced as one clean, actionable error instead of a raw dial failure repeated once per dependency.

## Non-goals
- No auto-starting Qdrant in this ticket — that's WATCH-016, deliberately split out since "detect and report" and "detect and fix" are different-sized pieces of work with different risk (this ticket is pure read-only probing, safe to ship alone).
- No change to `checkBackendReachable`/`probeBackend` (`internal/cli/doctor.go`/`internal/cli/status.go`) — those already do a live, on-demand reachability probe for `doctor`/`status` specifically, and must keep doing a *fresh* probe each time (a cached background state would be the wrong answer for a diagnostic command). This ticket adds a *second*, separate mechanism for the operations that actually try to *use* the backend (sync, GC, search), the same relationship WATCH-014 has between `embeddingReadiness` and the pre-existing `checkEmbeddingModel` doctor check.
- No change to `internal/backend`/`internal/backend/qdrant` — this is entirely a readiness-tracking and gating change in `internal/cli`, same layer WATCH-014 touched.

## Simplicity constraints
- Copy WATCH-014's shape exactly rather than inventing a new one: a `vectorReadiness` type (`internal/cli/vector_readiness.go`, new file, sibling to `embedding_readiness.go`) with the same `unknown`/`checking`/`ready`/`unreachable`/`error` states (no `pulling` state — there is no download step here, that's WATCH-016's concern), a `checkReady() error` method with nil-receiver safety, and a `checkVectorReadiness(ctx, cfg, readiness, logf)` background prober.
- Reuse `backend.VectorBackend.Health(ctx)` via the existing `buildVectorBackend`/`probeBackend` helpers (`internal/cli/pipeline.go`, `internal/cli/status.go:178`) for the actual reachability probe — no new HTTP client, no new probing logic.

## Design
`internal/cli/vector_readiness.go` (new):
```go
type vectorState string
const (
    vectorStateUnknown     vectorState = "unknown"
    vectorStateChecking    vectorState = "checking"
    vectorStateReady       vectorState = "ready"
    vectorStateUnreachable vectorState = "unreachable"
    vectorStateError       vectorState = "error"
)

type vectorReadiness struct { /* same mutex-guarded shape as embeddingReadiness */ }

func (r *vectorReadiness) checkReady() error // nil-safe, same contract as embeddingReadiness.checkReady

func checkVectorReadiness(ctx context.Context, cfg config.Config, readiness *vectorReadiness, logf func(format string, args ...any)) {
    readiness.set(vectorStateChecking, "")
    if err := probeBackend(ctx, cfg); err != nil {
        readiness.set(vectorStateUnreachable, fmt.Sprintf("%s not reachable at %s: %v", cfg.Vector.Backend, cfg.Vector.Endpoint, err))
        return
    }
    readiness.set(vectorStateReady, "")
}
```
Unlike `checkEmbeddingReadiness`, there's no provider branch (`buildVectorBackend` already rejects anything but `"qdrant"` per VEC-002's scope) and no pull step — this is deliberately the simpler half.

`engine` (`internal/cli/daemon.go`) gains a `vectorReadiness *vectorReadiness` field alongside its existing `embeddingReadiness` one. `runDaemonRun` constructs it and launches `go checkVectorReadiness(ctx, cfg, readiness, logf)` next to the existing `go checkEmbeddingReadiness(...)` call (~line 792-793).

Every call site that calls `buildVectorBackend` for real work — not a diagnostic probe — gates on `e.vectorReadiness.checkReady()` first, the same pattern WATCH-014 used for `buildEmbedder`:
- `internal/cli/sync.go`'s `RunSync`/`getPipeline` closure (~line 108, right after the existing `readiness.checkReady()` call for the embedder) — `RunSync` gains a second `*vectorReadiness` parameter.
- `internal/cli/daemon.go`'s `engine.fullQueryService` (~line 398, the search path) — checked before `buildVectorBackend`, same "outside the `sync.Once`" placement WATCH-014 used so a "still unreachable" answer is never memoized as the final build outcome.
- `internal/cli/gc.go`'s `RunGC` (~line 69, where it calls `buildVectorBackend(cfg)`) — `engine.GC` (`internal/cli/daemon.go:483`) is a one-line wrapper around `RunGC(ctx, e.store, e.badgerStore, e.cfg, dryRun, out)`; `RunGC` gains the same `*vectorReadiness` parameter `RunSync` does, and `engine.GC` passes `e.vectorReadiness`.

`api.Health` (`internal/daemon/api/api.go`) gains `VectorState`/`VectorDetail`, mirroring `EmbeddingState`/`EmbeddingDetail` exactly — read live from a new `Engine.VectorReadiness(ctx) (state, detail string)` method (`internal/daemon/server.go`), wired into `handleHealth` the same way. `depctl daemon status` gains a `vector:` line next to its existing `embedding:` one (`internal/cli/daemon.go`'s `runDaemonStatus`, reusing the `embeddingStatusLabel`-style formatter pattern).

## Inputs / Outputs
- Input: the daemon's own configured `vector.backend`/`endpoint` — no new config surface (WATCH-016 adds `vector.managed`, not this ticket).
- Output: `depctl daemon status`'s new `vector:` line; `/v1/health`'s `vector_state`/`vector_detail`; actionable errors from `sync`/GC/search instead of the raw `dial tcp ... connection refused` currently surfacing through `generation.Replicate`.

## Failure behavior
- Qdrant unreachable: `sync`/GC/search fail immediately with `"vector backend unreachable: qdrant not reachable at http://127.0.0.1:6333: <underlying error>"`, once per call rather than once per dependency inside a sync loop.
- Qdrant reachable at startup but goes down later: the background check only runs once at startup in this ticket (same as WATCH-014's embedding check) — a mid-session outage still surfaces as a real error from the actual `buildVectorBackend`/`Health` call at that point, just no longer a confusing raw dial error repeated per action, since `probeBackend`/`Health` already wraps it reasonably. (Periodic re-checking is an explicit non-goal here, matching WATCH-014's own scope — see that ticket's Non-goals for the same reasoning.)

## Tests
- `vectorReadiness.checkReady()` per state, nil-receiver safety — mirrors `TestEmbeddingReadinessCheckReadyPerState`/`TestEmbeddingReadinessCheckReadyNilIsAlwaysReady` (`internal/cli/embedding_readiness_test.go`) exactly.
- `checkVectorReadiness` against a real `httptest.Server` standing in for Qdrant (unreachable and reachable cases) — mirrors `TestCheckEmbeddingReadinessUnreachable`/`TestCheckEmbeddingReadinessAlreadyPulled`.
- Real-daemon integration test extending `TestDaemonStartsWithUnreachableServices` (`internal/cli/daemon_test.go:168`, already exercises `deadEndpointsConfig` for both embedding and vector endpoints) to also assert `health.VectorState == "unreachable"`.
- Real-daemon integration test mirroring `TestSyncReportsStructuredErrorForUnreachableEmbeddingBackend` (`internal/cli/sync_test.go`): a `sync` against a dead vector endpoint (with a *reachable* embedding endpoint, to isolate which readiness check is actually firing) reports the structured vector-unreachable message.

## Acceptance criteria
- [x] The daemon checks vector-backend reachability in the background at startup, never on a client's request path.
- [x] `sync`, GC, and search fail fast with an actionable message while Qdrant is unreachable, instead of a raw dial error (repeated per dependency, in sync's case).
- [x] `depctl daemon status` and `/v1/health` surface the live state, next to the existing embedding readiness line.
- [x] `checkBackendReachable`/`probeBackend` (doctor/status's existing live probes) are unchanged — this ticket adds a second, separate mechanism, not a replacement.

## Post-implementation note
Shipped exactly as specced: `internal/cli/vector_readiness.go` mirrors `embedding_readiness.go`'s shape (no `pulling` state, as planned — no download step). `RunSync`/`RunGC` both gained a trailing `*vectorReadiness` parameter; `fullQueryService` checks it outside `fullQueryOnce.Do`, same memoization-safety pattern as WATCH-014. `api.Health` gained `VectorState`/`VectorDetail`; `daemon status` gained a `vector:` line via `vectorStatusLabel`. New tests: `vector_readiness_test.go` (mirrors `embedding_readiness_test.go`), an extension to `TestDaemonStartsWithUnreachableServices`, and `TestSyncReportsStructuredErrorForUnreachableVectorBackend` (mirrors the embedding version, using a fake Ollama server to isolate which readiness check fires). Full suite green.

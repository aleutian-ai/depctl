# WATCH-014: Daemon-owned embedding-backend readiness

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-013 (MCP progress notifications), the MCP-bootstrapping fix (`docs/scratch/mcp-bootstrapping.md`)
**Estimated size:** medium

## Goal
An MCP-first user who registers `ragctl serve` on a fresh machine with Ollama installed but missing the embedding model should never have to leave their agent to run `ollama pull` themselves — but the fix must not turn the daemon's very first request into an unexplained multi-minute hang. Split "check whether the embedding backend is ready" from "install software": ragctl auto-pulls its own embedding *model* through an already-running Ollama (the same category of unprompted fetch it already does for git dependencies), but never attempts to install the Ollama *runtime* itself, and never blocks a client request while doing either.

## Non-goals
- No Ollama-runtime installation of any kind — out of ragctl's wedge entirely (see the design discussion this ticket resolves, captured in `docs/scratch/mcp-bootstrapping.md`'s history and this ticket's own reasoning below).
- No blocking prereq check inside `ragctl init` or `ensureInitialized`/`ensureDaemon`'s auto-init path — an earlier attempt at this (auto-pull synchronously inside `initStores`) was built and explicitly reverted mid-session: it has no way to report progress before a daemon/MCP session even exists, and a human-facing `ragctl init` blocking on a model download reads as broken, not helpful.
- No containerized backend (`ragctl backend up`) — a reasonable idea, but wrong scope for this ticket; tracked separately in `docs/tickets/backlog/44-containerized-embedding-backend/`.

## Design
`internal/cli/embedding_readiness.go` (new): an `embeddingReadiness` type (`unknown`/`checking`/`pulling`/`ready`/`unreachable`/`error`, mutex-guarded) and `checkEmbeddingReadiness(ctx, cfg, readiness, logf)`, which probes Ollama's `/api/tags` (reachability, then whether the configured model is present) and calls the new `ollama.Client.PullModel` if it's missing — all off any client's request path.

`runDaemonRun` (`internal/cli/daemon.go`) launches this as `go checkEmbeddingReadiness(ctx, cfg, readiness, logf)` right after the daemon's own context exists, alongside the engine construction — a background task exactly like `startWatch`, not a request-triggered one.

Every request path that would otherwise build an embedder checks `readiness.checkReady()` first and returns one of three fast, honest answers instead of ever attempting the build itself while not ready:
- ready → proceeds normally (buildEmbedder unchanged, EMB-002's scope untouched)
- unreachable → `"embedding backend unreachable: Ollama not reachable at <endpoint> — start it, then it'll be rechecked..."`
- pulling → `"ragctl is initializing its embedding backend: <model> is being downloaded by the local daemon — retry shortly, or run \`ragctl daemon status\` for progress"`

Wired into `internal/cli/sync.go`'s `RunSync` (the lazy `getPipeline` closure) and `internal/cli/daemon.go`'s `engine.fullQueryService` (the search path) — the two and only two places that ever call `buildEmbedder`. The readiness check runs on every call, never memoized alongside the actual pipeline build, so a "still pulling" answer doesn't get cached forever once the model finishes downloading.

`api.Health` gained `EmbeddingState`/`EmbeddingDetail`, read live from the `Engine` interface's new `EmbeddingReadiness(ctx)` method on every `/v1/health` call (unlike `ConfigFingerprint`, which is fixed at daemon startup) — surfaced in `ragctl daemon status`'s new `embedding:` line.

`internal/embedding/ollama.Client` gained `Reachable`, `ModelPulled`, and `PullModel` — small, focused additions to the existing hand-written HTTP client, no new dependency.

## Inputs / Outputs
- Input: the daemon's own configured `embedding.provider`/`model`/`endpoint` — no new config surface.
- Output: `ragctl daemon status`'s `embedding:` line; `/v1/health`'s `embedding_state`/`embedding_detail`; actionable errors from `sync`/search instead of raw connection errors while not ready.

## Failure behavior
- Ollama unreachable: `sync`/search fail immediately with a message naming the fix, never a raw connection-refused error from inside `buildEmbedder`.
- Model pull itself fails (network drop, model doesn't exist): readiness moves to `error` with the failure detail and the manual fallback command (`ollama pull <model>`), not left stuck in `pulling` forever.
- A provider other than `"ollama"` is marked ready immediately — nothing here understands another provider's reachability semantics, matching `buildEmbedder`'s own EMB-002 scope limit.

## Tests
- `embeddingReadiness.checkReady()` per state, including nil-receiver safety (any caller with no readiness tracking is always "ready").
- `checkEmbeddingReadiness` against a real `httptest.Server`: unreachable, already-pulled, missing-then-pulled, and non-ollama-provider cases.
- `ollama.Client.Reachable`/`ModelPulled`/`PullModel` unit tests, including Ollama's own reported pull error surfacing correctly.
- Real-daemon integration test (`TestDaemonStartsWithUnreachableServices`, extended): the background check reaches `unreachable` on its own with no client action.
- Real-daemon integration test (`TestSyncReportsStructuredErrorForUnreachableEmbeddingBackend`, new): a `sync` against a dead embedding endpoint reports the structured message, not a raw error.

## Acceptance criteria
- [x] The daemon checks embedding-backend readiness in the background at startup, never on a client's request path.
- [x] `sync` and search fail fast with an actionable message while not ready, instead of hanging or surfacing a raw HTTP error.
- [x] A "still pulling" result is never memoized as a permanent failure — later calls see the real, current state.
- [x] `ragctl daemon status` and `/v1/health` surface the live state.
- [x] `ragctl init`'s own flow is untouched — no auto-pull, no reachability check, matching the explicit decision to keep that path exactly as it was before this ticket.

## Post-implementation note

The design went through one real iteration worth recording: the first attempt put the reachability check and a blocking pull directly inside `initStores` (shared by both `ragctl init` and `ensureInitialized`'s silent MCP auto-init path), gated behind a user confirmation on whether it should run in both paths. It was built, verified live, and then explicitly reverted — not because auto-pulling a model is wrong (it isn't; that's exactly what this ticket does), but because blocking synchronously *before a daemon exists* has no way to report progress to the eventual caller, especially the MCP path where that would silently hang a fresh agent session's very first call. Moving the check into the daemon itself (already the one long-lived, background-task-owning process) resolved that with no compromise on the actual goal: the model still gets pulled automatically, MCP-first still works with zero manual steps when Ollama is already installed, `ragctl init` stays exactly as fast and side-effect-free as it always was, and no client call ever blocks on a live download.

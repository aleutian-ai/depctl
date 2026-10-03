# MEM0-001: Mem0 export connector

**Epic:** Cross-Agent Memory System Connectors
**Status:** done (2026-09-30)
**Depends on:** none (reads only already-shipped storage: `internal/data/badger`, `internal/query`)
**Estimated size:** medium

## Goal
`ragctl export mem0 --project <id> [--dependency <name>]` pushes a project's (or one dependency's) already-synced, already-validated dependency knowledge into a user's own Mem0 instance as tagged memories, so an agent using Mem0 for cross-session memory surfaces ragctl's version-correct facts through its own normal memory search — without needing a second MCP tool call to ragctl.

## Non-goals
- No automatic/background export on sync completion — explicit command only.
- No import path (Mem0 → ragctl).
- No Mem0 account/instance management — the user supplies their own endpoint/API key, the same way `vector.endpoint`/`vector.api_key_env` already work for an unmanaged Qdrant.
- Not a dependency of ragctl's core sync/query path — this command opens no new code path unless explicitly invoked; the daemon's own behavior is unaffected whether or not this command ever runs.
- No attempt to replicate Mem0's own memory-extraction/deduplication logic — ragctl hands over real, already-chunked content with real metadata; whatever Mem0 does with it internally is Mem0's own concern.

## Architecture — daemon-owned (ADR-011)
Per ADR-011, the daemon is the only process that opens `internal/data/badger`; the CLI never reads it directly. So this is a new daemon endpoint, not a CLI-side Badger read, following the exact existing `/v1/sync` pattern (`internal/daemon/api/api.go`, `internal/daemon/server.go`, `internal/daemon/handlers.go`, `internal/cli/daemon.go`'s `engine`, `internal/daemon/client/client.go`):

- `api.PathExportMem0 = "/v1/export/mem0"`, `api.ExportMem0Request`/`api.ExportMem0Response` in `internal/daemon/api/api.go`.
- `Engine.ExportMem0(ctx, req) (*api.ExportMem0Response, error)` added to the `Engine` interface (`internal/daemon/server.go`), implemented on `engine` in `internal/cli/daemon.go` — reads `e.badgerStore` directly, in-process, then does the outbound HTTP push to the user's Mem0 instance from inside that method. No new store-access pattern; it uses the same `badgerStore.ListGenerationChunks`/`GetKnowledgeObject` calls `generation.Replicate` already uses.
- `mux.HandleFunc("POST "+api.PathExportMem0, s.handleExportMem0)` in `routes()`; `handleExportMem0` in `handlers.go` follows `handleSync`'s shape (`decodeBody`, call `Engine.ExportMem0`, stream progress + terminal result via `stream(...)` since a large export is long-running, same as sync).
- `Client.ExportMem0(ctx, req, out)` in `internal/daemon/client/client.go`, using `c.stream` (progress-bearing, same as `Client.Sync`).
- `ragctl export mem0` (`internal/cli/export.go`) is a thin CLI command: parse flags, build `api.ExportMem0Request`, call `Client.ExportMem0`, print the streamed progress/summary — it never touches Badger or the Mem0 HTTP client itself.

## Transport — originally assumed (2026-09-30), superseded
**Superseded by the real-container verification below (2026-10-03):** everything in this section describes the *hosted* Mem0 Platform's API, not the self-hosted server this connector is for.

`POST /v3/memories/add/` (`docs.mem0.ai/api-reference/memory/add-memories`) — not `/v1/...`, verified directly against current docs rather than assumed. Request body: `messages` (array of `{role, content}`), one of `user_id`/`agent_id`/`app_id`/`run_id` (this connector always sends `user_id`, set to the project ID — the same scoping role Graphiti's `group_id` plays), optional `metadata`. **By default this endpoint is asynchronous** — it returns `{"event_id": ..., "status": "PENDING"}` immediately, with no success/failure signal until a separate `GET /v1/event/{event_id}/` poll. That's incompatible with this ticket's per-chunk success/failure reporting, so every request sets `"infer": false`, which makes the call synchronous and returns `results`/`message` directly — confirmed in the same docs page.

## Simplicity constraints
- New package `internal/export/mem0` — a plain Go HTTP client against Mem0's REST API, not their Python/TS SDK; matches how ragctl already treats every other external HTTP integration (registry lookups, Qdrant's own adapter). Used from inside `engine.ExportMem0`, not from the CLI process.
- Reads exclusively through existing storage APIs — `badgerStore.ListGenerationChunks` (already used by `hack/verify-sync`/`generation.Replicate`) and `badgerStore.GetKnowledgeObject` — no new acquisition, chunking, or embedding logic of any kind.
- Its own endpoint, request/response types, and `Engine` method — duplicated from `GRAPHITI-001`/`LETTA-001`/`COGNEE-001`'s own daemon wiring rather than factored into a shared `ExportTarget` interface; each connector's daemon-side code stays simple and self-contained.

## Design
Package: `internal/export/mem0` (invoked by `engine.ExportMem0`, daemon-side)

```go
type Client struct { /* base URL, API key */ }

func NewClient(baseURL, apiKey string) *Client
func (c *Client) AddMemory(ctx context.Context, userID, text string, metadata map[string]string) error // POST /v3/memories/add/, infer:false
func (c *Client) Health(ctx context.Context) error // GET /v1/entities/ — Mem0 has no dedicated health endpoint
```

`engine.ExportMem0` (`internal/cli/daemon.go`):
1. Resolve the target generation(s) the same way `ragctl status`/`describe` already do (active generation per dependency, or one named dependency via `--dependency`).
2. `ListGenerationChunks` for each, batching requests to respect Mem0's own rate limits (a plain bounded worker pool, matching `Sync.MaxConcurrency`'s existing shape — no new concurrency primitive).
3. Map each chunk to one Mem0 memory: `text` = chunk content, `metadata` = `{ecosystem, dependency, version, generation, source_type, authority, trust_class}` — the exact same fields OBS-001's structured logs and `search_dependency_docs`'s own chunk output already carry, so nothing here invents a new vocabulary.
4. Report a per-chunk success/failure summary via the stream (matching `ragctl sync`'s own OK/FAIL-per-item convention) — a partial failure never aborts the whole batch.

## Inputs / Outputs
- Input: `--project`, optional `--dependency`; Mem0 endpoint + API key via config (`export.mem0.endpoint`, `export.mem0.api_key_env`) or flags — read daemon-side from the already-loaded `config.Config`, same as every other daemon-side setting.
- Output: memories created in the user's own Mem0 instance; a text summary streamed to the CLI's stdout.

## Failure behavior
- Mem0 unreachable/misconfigured → clean, actionable error before any chunk is attempted (a `Health`-style pre-flight check, matching `vectorReadiness`'s own pattern), returned as the terminal `api.Error` if it happens before streaming starts.
- One chunk's push failing does not abort the rest of the batch — reported per-chunk, same as `ragctl gc`'s own per-candidate OK/FAIL summary.

## A real, disclosed fact about Mem0 (see epic's own note)
Mem0's self-hosted mode has telemetry on by default, with open upstream issues reporting the opt-out doesn't fully work (mem0ai/mem0 #3762, #3729, #2683). This connector's own `--help` text and README section must say so plainly — a user choosing to run this command is choosing to send data to Mem0; what Mem0's own SDK/telemetry does with that afterward is disclosed, not hidden, never framed as a ragctl policy exception (it isn't one — `SEC-005` governs ragctl's own core behavior, not a third-party service a user explicitly invoked).

## Tests
- A fake Mem0 HTTP server (`httptest`, matching every other external-HTTP test in this codebase) verifying the exact request shape (text + metadata fields) and batching behavior.
- A partial-failure test: one chunk's push fails, the rest still succeed, summary reports both correctly.
- A real generation (via the existing git-fixture pattern from `internal/data/generation`'s own tests) exported end-to-end against the fake server, confirming the metadata mapping matches real chunk/object fields exactly.
- A daemon-level test (matching the existing `internal/cli/daemon_test.go` harness) confirming `POST /v1/export/mem0` streams progress and a terminal summary, and that the CLI command never opens Badger itself.

## Acceptance criteria
- [x] `ragctl export mem0 --project <id>` pushes every active-generation chunk for that project, with correct metadata, verified against a real fake-server request capture (`internal/cli/export_test.go`'s `TestExportMem0PushesEveryChunkWithCorrectMetadata`, a real resolution/generation/chunk fixture exported end-to-end against an `httptest` Mem0 double).
- [x] `--dependency <name>` scopes to one dependency only (`api.ExportMem0Request.Dependencies`, `--dependency` flag, filtered in `engine.ExportMem0`).
- [x] Partial failures are reported per-chunk, never abort the batch (`TestExportMem0PartialFailureDoesNotAbortBatch`).
- [x] README/`--help` text plainly discloses Mem0's own telemetry behavior (`ragctl export mem0 --help`'s `Long` text, `internal/cli/export.go`).

## Post-implementation note (2026-09-30)
Shipped per the epic's daemon-owned architecture decision: `POST /v1/export/mem0` (`internal/daemon/api`, `internal/daemon/server.go`, `internal/daemon/handlers.go`), `engine.ExportMem0` (`internal/cli/daemon.go`), `Client.ExportMem0` (`internal/daemon/client/client.go`), `ragctl export mem0` (`internal/cli/export.go`), and the `internal/export/mem0` HTTP client. Two corrections made during implementation, not assumed from the original ticket text:
- The real endpoint is `POST /v3/memories/add/`, not `/v1/...` — verified directly against `docs.mem0.ai/api-reference/memory/add-memories` rather than guessed.
- That endpoint is asynchronous by default (returns `event_id`/`PENDING`, no immediate success/failure signal) — incompatible with this ticket's per-chunk reporting, so every request sets `"infer": false` to get a synchronous response.
- Mem0 has no dedicated health/ping endpoint (confirmed absent from its API reference) — `Health()` uses `GET /v1/entities/` instead, the cheapest real, documented, read-only endpoint available.

All acceptance criteria boxes are now checked.

## Real-container verification (2026-10-03)
Tested against Mem0's real self-hosted server, built from `github.com/mem0ai/mem0`'s `server/` (no published image exists), with `pgvector/pgvector:pg17`, under Podman. It ran fully local: Mem0's bundled OpenAI provider was pointed at Ollama's OpenAI-compatible `/v1` (`OPENAI_BASE_URL`), with a 768-dim `nomic-embed-text` embedder set via `POST /configure`. No paid key.

**The connector had been built against the wrong API.** The paths and auth in the original Transport section are the hosted Mem0 Platform's. The self-hosted server (`server/main.py`) uses `POST /memories`, `GET /entities`, and `X-API-Key` auth. Live: the old `Authorization: Token` header got 401, and the old health path (`/v1/entities/`) got 404, which the old check would have passed, since it accepted anything below 500. The request body happened to match (`messages`, `user_id`, `metadata`, `infer`). Fixed: `POST /memories`, `X-API-Key`, and a health check (`GET /entities`) that requires a real 200, with specific messages for 401 (key) and 404 ("is this a self-hosted Mem0 server?"). The hosted Platform is explicitly unsupported.

**Three more real findings, fixed:**
1. **Re-exporting duplicated everything.** With `infer: false`, Mem0 stores every add (168 rows, 84 distinct after two exports). Each memory is now tagged `run_id = "ragctl:<dependency>"`, and exporting a dependency first deletes that project+dependency's previous memories (`DELETE /memories?user_id=…&run_id=…`, filters ANDed), so only ragctl's own writes are touched. If the delete fails (e.g. a non-admin key; self-hosted Mem0 requires admin for deletes), nothing is pushed for that dependency, so a duplicate set is never left behind. Live: two exports → 84; moving the project from `uuid` v1.6.0 to v1.5.0 and exporting → exactly 81, all v1.5.0.
2. **Exported version labels could be wrong.** Metadata `version` was read from the knowledge object, but GEN-003 content reuse means a reused object keeps the version of whichever build first created it (the same trap `generation.Replicate` documents). Live: exporting v1.5.0 labeled 72 of 81 chunks `v1.6.0`. Ecosystem, dependency, and version now come from the generation being exported. Residual, documented: `source_type`/`authority`/`trust_class` still come from the object, and could only go stale if a dependency's *sources* changed between versions.
3. **Misleading auth errors.** A rejected key was reported as "mem0 unreachable"; it now reads "pre-flight check failed … authentication failed". And `--api-key-env` naming a variable the **daemon** doesn't have now says exactly that (the daemon reads it, not the shell), instead of letting the server answer 401. The same two wording fixes apply to the Graphiti and Cognee connectors.

**Verified end to end:** `ragctl export mem0` pushed 84 chunks in 6s with 0 failures; Postgres held all 84 (Mem0's list endpoint shows 20 by default, a page size, not data loss); and Mem0's own semantic search for "How do I generate a new random UUID?" returned `uuid`'s `New`/`NewRandom` docs with ragctl's version, generation, and trust metadata intact.


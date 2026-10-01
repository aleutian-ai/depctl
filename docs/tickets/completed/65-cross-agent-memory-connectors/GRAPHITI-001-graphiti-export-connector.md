# GRAPHITI-001: Graphiti export connector

**Epic:** Cross-Agent Memory System Connectors
**Status:** done (2026-10-01)
**Depends on:** none (reads only already-shipped storage: `internal/data/badger`, `internal/query`)
**Estimated size:** medium

## Goal
`ragctl export graphiti --project <id> [--dependency <name>]` pushes a project's (or one dependency's) already-synced dependency knowledge into a user's own Graphiti instance as structured JSON episodes, so an agent using Graphiti's temporal knowledge graph for cross-session memory gets ragctl's exact API-shape facts (function signatures, parameters, types) represented as real graph entities/relationships, not just flat text.

## Non-goals
- No automatic/background export — explicit command only, same as `MEM0-001`.
- No import path (Graphiti → ragctl).
- No bundling or managing Neo4j — the user supplies their own already-running Graphiti/Neo4j instance.
- Not a dependency of ragctl's core sync/query path — same isolation guarantee as `MEM0-001`.
- No attempt to control or second-guess Graphiti's own LLM-based entity/relationship extraction over the submitted JSON — ragctl hands over real structured facts; what Graphiti's own pipeline does with them afterward is Graphiti's concern.

## Transport — confirmed (2026-09-30), plain REST, not MCP
Graphiti has a real, separate, self-hostable REST API distinct from its MCP server and from Zep Cloud (the commercial hosted product): `server/graph_service` in `github.com/getzep/graphiti`, a FastAPI app, distributed as the `zepai/graphiti` Docker image, configured against the user's own Neo4j/FalkorDB backend + LLM key. Relevant endpoints (`server/graph_service/routers/ingest.py`/`retrieve.py`):

- `POST /messages` — episode ingestion (the real endpoint name; not literally `/add_episode`, though it's the same underlying operation the Python SDK/MCP tool calls `add_episode`).
- `POST /search`, `GET /episodes/{group_id}`, `POST /get-memory` — read-side, not needed by this ticket (export only).
- `DELETE /episode/{uuid}`, `DELETE /group/{group_id}`, `POST /clear` — destructive; this connector never calls these.

This settles the design in `MEM0-001`'s favor: a plain HTTP client, no `sdkmcp` client dependency needed for this connector at all.

**A real, disclosed security fact, found during this verification**: the self-hosted REST service ships with **no authentication by default** — a real, open upstream issue (`getzep/graphiti#1716`) reports every endpoint, including the destructive delete/clear ones, is reachable by anyone who can reach the port. This connector must never assume the target is safe to expose beyond localhost/a private network; its own docs must say so plainly (same disclosure obligation as `MEM0-001`'s telemetry note), and the connector itself should support an optional bearer-token/header config for the (increasingly likely) case a user puts their own auth in front of it via a reverse proxy — never invented or assumed present by default.

## Architecture — daemon-owned (ADR-011)
Same reasoning and wiring as `MEM0-001`'s own "Architecture" section: the CLI never opens Badger directly, this is a new daemon endpoint following the existing `/v1/sync` pattern.

- `api.PathExportGraphiti = "/v1/export/graphiti"`, `api.ExportGraphitiRequest`/`api.ExportGraphitiResponse` in `internal/daemon/api/api.go`.
- `Engine.ExportGraphiti(ctx, req) (*api.ExportGraphitiResponse, error)` added to the `Engine` interface (`internal/daemon/server.go`), implemented on `engine` in `internal/cli/daemon.go` — reads `e.badgerStore` directly, in-process, then pushes episodes to the user's Graphiti instance from inside that method.
- `mux.HandleFunc("POST "+api.PathExportGraphiti, s.handleExportGraphiti)` in `routes()`; `handleExportGraphiti` in `handlers.go` follows `handleSync`'s shape (streamed progress + terminal result).
- `Client.ExportGraphiti(ctx, req, out)` in `internal/daemon/client/client.go`, using `c.stream`.
- `ragctl export graphiti` (`internal/cli/export.go`) is a thin CLI command: parse flags, build `api.ExportGraphitiRequest`, call `Client.ExportGraphiti`, print the streamed progress/summary.

## Simplicity constraints
- New package `internal/export/graphiti` — a plain Go HTTP client against `POST /messages`, matching `MEM0-001`'s own shape exactly (same package structure, same batching/pre-flight pattern) — no `sdkmcp` client, no new transport primitive this codebase doesn't already have for HTTP. Used from inside `engine.ExportGraphiti`, not from the CLI process.
- Reads exclusively through existing storage APIs — same as `MEM0-001` (`ListGenerationChunks`, `GetKnowledgeObject`) — no new acquisition/chunking/embedding logic.
- Its own endpoint, request/response types, and `Engine` method — duplicated from `MEM0-001`/`LETTA-001`/`COGNEE-001`'s own daemon wiring rather than factored into a shared `ExportTarget` interface; each connector's daemon-side code stays simple and self-contained. CLI-flag parsing for `--project`/`--dependency` is likewise its own small block in `export.go`, not a shared helper.

## Design
Package: `internal/export/graphiti` (invoked by `engine.ExportGraphiti`, daemon-side)

```go
type Client struct { /* base URL, optional bearer token */ }

func NewClient(baseURL, authTokenEnv string) *Client
func (c *Client) AddEpisode(ctx context.Context, groupID string, content json.RawMessage) error // POST /messages
```

Each dependency's set of chunks is submitted as one JSON episode per generation (not per chunk — Graphiti's own extraction pipeline works over a coherent document, not isolated fragments), shaped as:

```json
{
  "dependency": "google.golang.org/protobuf",
  "version": "v1.36.11",
  "ecosystem": "go",
  "chunks": [
    {"content": "...", "source_type": "git", "trust_class": "repository", "breadcrumb": "..."}
  ]
}
```

`group_id` (Graphiti's own multi-tenant scoping field, part of the real `/messages` request body) is set to the project ID, so a user with multiple ragctl-tracked projects can scope Graphiti queries per project the same way ragctl itself does.

## Inputs / Outputs
- Input: `--project`, optional `--dependency`; Graphiti REST base URL via config (`export.graphiti.endpoint`) or flags, plus an optional bearer-token env var (`export.graphiti.auth_token_env`) for a user-supplied reverse-proxy auth layer — never assumed present, since the service has none of its own by default.
- Output: episodes created in the user's own Graphiti instance via `POST /messages`; a text summary to stdout, same convention as `MEM0-001`.

## Failure behavior
- Graphiti/Neo4j unreachable → clean, actionable pre-flight error before any episode is attempted, same pattern as `MEM0-001`'s own `Health`-style check.
- One dependency's episode failing does not abort the rest of the batch.

## Tests
- A fake HTTP server (`httptest`, matching `MEM0-001`'s own test shape) verifying the exact `POST /messages` request body, including `group_id`.
- A partial-failure test, same shape as `MEM0-001`'s.
- A test confirming the optional bearer token, when configured, is sent as an `Authorization` header — and that its absence doesn't break requests against a target with no auth (the real, disclosed default).
- A daemon-level test confirming `POST /v1/export/graphiti` streams progress and a terminal summary, and that the CLI command never opens Badger itself.

## Acceptance criteria
- [x] `ragctl export graphiti --project <id>` pushes one episode per active generation via `POST /messages`, with the JSON shape above, verified against a real fake-server request capture (`internal/cli/export_test.go`'s `TestExportGraphitiPushesOneEpisodePerDependency`).
- [x] `--dependency <name>` scopes to one dependency only (`api.ExportGraphitiRequest.Dependencies`, filtered in `engine.ExportGraphiti` via the shared `nameSet` helper).
- [x] Partial failures are reported per-dependency, never abort the batch (one `api.ExportGraphitiResult` per dependency; a failed episode doesn't stop the loop).
- [x] README/`--help` text plainly discloses that self-hosted Graphiti has no built-in authentication by default (`getzep/graphiti#1716`) and that exposing it beyond localhost/a private network is the user's own responsibility (`ragctl export graphiti --help`'s `Long` text, `internal/cli/export.go`).

## Post-implementation note (2026-10-01)
Shipped per the epic's daemon-owned architecture decision, same shape as `MEM0-001`: `POST /v1/export/graphiti` (`internal/daemon/api`, `server.go`, `handlers.go`), `engine.ExportGraphiti` (`internal/cli/daemon.go`), `Client.ExportGraphiti` (`internal/daemon/client/client.go`), `ragctl export graphiti` (`internal/cli/export.go`), and the `internal/export/graphiti` HTTP client. (Superseded by the real-container verification below: `Health()` originally used `GET /episodes/{group_id}`, on the mistaken belief Graphiti had no health endpoint.)

## Real-container verification (2026-10-01)
Run against a real self-hosted `zepai/graphiti:latest` plus `neo4j:5.22.0` under Podman, with Graphiti's LLM pointed at local Ollama through its OpenAI-compatible API (`OPENAI_BASE_URL`, `MODEL_NAME=ministral-3:3b`, placeholder `OPENAI_API_KEY`). The real `ragctl` binary exported a really-synced `github.com/google/uuid` v1.6.0 generation. Findings, read from Graphiti's own source (`server/graph_service`) and confirmed live:

**Two real connector bugs, fixed. Neither was catchable by the fake-server tests, which accepted any request shape:**
1. **Every export failed with `422`.** Graphiti's `Message` model requires `role_type` (`user`/`assistant`/`system`) and `role`, and the connector sent neither. It now sends `role_type: "system"` (reference documentation, not a conversational turn) and `role: "ragctl"`. After the fix, `POST /messages` returns `202`.
2. **The health check never checked anything.** `GET /episodes/{group_id}` requires a `last_n` query parameter, so it always returned `422`, which "passed" only because any non-5xx counted as healthy. Graphiti has a real `GET /healthcheck` endpoint, which `Health()` now uses, and it requires an actual `200`.

**Verified:** wire-level correctness. The request is accepted (`202`) by a real server, and the payload, `group_id`, and episode shape match what Graphiti expects.

**Not verified: end-to-end ingestion.** `POST /messages` only *queues* the episode (a background `AsyncWorker`), so a `202` says nothing about whether it lands. In this run, Graphiti's worker called the local model, which finished within seconds, then stored nothing: zero episodes, an empty Neo4j, and no error logged. Graphiti saves an episode only after its LLM extraction succeeds, and its worker loop catches only `CancelledError`. A failed extraction therefore fails silently, and may stop the worker from processing later jobs until a restart. The likely cause is the small local 3B model failing Graphiti's structured-output extraction over one large episode, but that wasn't pinned down. Confirming ingestion end to end needs a stronger model or a real OpenAI key, and is left open.

**Upstream image bug, disclosed and not worked around in ragctl:** `zepai/graphiti:latest` (arm64) runs as user `app` but launches `uv` from `/root/.local/bin`, so the container fails at start with `Permission denied`. The test ran it with `--user root`.

**Design concern, worth revisiting:** one episode per dependency holds that dependency's *entire* chunk set. That was fine for `google/uuid`, but a large dependency (e.g. `google.golang.org/protobuf`, about 15k chunks) would produce an episode far beyond any model's context window. If Graphiti export sees real use, it should probably batch chunks into several episodes per dependency.

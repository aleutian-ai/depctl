# COGNEE-001: Cognee export connector

**Epic:** Cross-Agent Memory System Connectors
**Status:** done (2026-10-01)
**Depends on:** none (reads only already-shipped storage: `internal/data/badger`, `internal/query`)
**Estimated size:** medium

## Goal
`depctl export cognee --project <id> [--dependency <name>]` pushes a project's (or one dependency's) already-synced dependency knowledge into a user's own Cognee instance as a dataset it can run its own extract-cognify-load (ECL) pipeline over, so an agent using Cognee for cross-session memory/knowledge-graph recall surfaces depctl's version-correct facts too — same one-way push pattern as `MEM0-001`/`GRAPHITI-001`/`LETTA-001`.

## Non-goals
- No automatic/background export — explicit command only, same as the epic's other connectors.
- No import path (Cognee → depctl).
- No management of Cognee's own storage backends (it can run over Postgres/pgvector, Neo4j, etc. depending on config) — the user supplies their own already-running instance.
- Not a dependency of depctl's core sync/query path — same isolation guarantee as the epic's other connectors.
- No attempt to control or second-guess Cognee's own `cognify` pipeline (entity/relationship extraction, embedding, graph construction) over the submitted content — depctl hands over real structured chunks; what Cognee's own pipeline does with them afterward is Cognee's concern.

## Transport — confirmed (2026-09-30), plain REST
Cognee has a real, self-hostable REST API server, distributed as the `cognee/cognee` Docker image (`docker compose up --build cognee`), with interactive docs at `/docs` and a base path of `http://localhost:8000/api/v1` (`docs.cognee.ai/guides/deploy-rest-api-server`). Relevant endpoints:

- `POST /api/v1/add` — upload content into a named dataset.
- `POST /api/v1/cognify` — trigger the ECL pipeline (`datasets`, `chunk_size` params) over previously-added content in that dataset.

This connector calls both in sequence per export: `add` then `cognify`. The `cognify` call is Cognee's own async pipeline trigger, not something this connector waits on synchronously — this connector's own HTTP client uses a normal request timeout for the two calls it makes and does not attempt to poll the pipeline to completion, keeping depctl's own side bounded regardless of how long Cognee's own processing takes.

**A real, disclosed fact, found during this verification**: Cognee's own `cognify` pipeline makes a blocking, timeout-less telemetry call (`send_telemetry()` in `cognee/shared/utils.py`) that can stall the pipeline for 60-120 seconds if the telemetry endpoint is unreachable (`github.com/topoteretes/cognee/issues/2120`, open as of this writing). This is Cognee's own operational issue, not something this connector can fix, but its docs must disclose it plainly alongside the telemetry-privacy point below, since a user running this connector against an offline/air-gapped Cognee instance could otherwise mistake a long `cognify` delay for a depctl bug.

**Telemetry, confirmed on by default**: Cognee sends anonymous telemetry (event name, user ID, properties) via `send_telemetry()` unless `TELEMETRY_DISABLED=1` is set on the Cognee server's own environment before import — this is the target service's own behavior, not depctl's (same non-conflict-with-`SEC-005` reasoning as Mem0's telemetry note); this connector's own docs disclose it plainly rather than silently.

**A real, disclosed security fact**: Cognee is **open by default** — `ENABLE_BACKEND_ACCESS_CONTROL` gates API-key validation and `REQUIRE_AUTHENTICATION` gates registration/login; neither is on by default. Same disclosed-gap shape as `GRAPHITI-001`/`LETTA-001`. This connector supports an optional bearer/API-key config field, never assuming auth is present.

**Scoping**: Cognee has a real `datasets`/`dataset_names` concept (plus a `/api/v1/permissions/datasets/` API for multi-tenant access control, not needed by this ticket) — the project ID maps to a dataset name, the same role Mem0's metadata tagging and Graphiti's `group_id` play for their own connectors.

## Architecture — daemon-owned (ADR-011)
Same reasoning and wiring as `MEM0-001`'s own "Architecture" section: the CLI never opens Badger directly, this is a new daemon endpoint following the existing `/v1/sync` pattern.

- `api.PathExportCognee = "/v1/export/cognee"`, `api.ExportCogneeRequest`/`api.ExportCogneeResponse` in `internal/daemon/api/api.go`.
- `Engine.ExportCognee(ctx, req) (*api.ExportCogneeResponse, error)` added to the `Engine` interface (`internal/daemon/server.go`), implemented on `engine` in `internal/cli/daemon.go` — reads `e.badgerStore` directly, in-process, then calls `add`/`cognify` against the user's Cognee instance from inside that method.
- `mux.HandleFunc("POST "+api.PathExportCognee, s.handleExportCognee)` in `routes()`; `handleExportCognee` in `handlers.go` follows `handleSync`'s shape (streamed progress + terminal result).
- `Client.ExportCognee(ctx, req, out)` in `internal/daemon/client/client.go`, using `c.stream`.
- `depctl export cognee` (`internal/cli/export.go`) is a thin CLI command: parse flags, build `api.ExportCogneeRequest`, call `Client.ExportCognee`, print the streamed progress/summary.

## Simplicity constraints
- New package `internal/export/cognee` — a plain Go HTTP client against `POST /api/v1/add` and `POST /api/v1/cognify`, matching the epic's other connectors' own shape (same package structure, same batching/pre-flight pattern) — no SDK dependency. Used from inside `engine.ExportCognee`, not from the CLI process.
- Reads exclusively through existing storage APIs — same as the epic's other connectors (`ListGenerationChunks`, `GetKnowledgeObject`) — no new acquisition/chunking/embedding logic.
- Its own endpoint, request/response types, and `Engine` method — duplicated from `MEM0-001`/`GRAPHITI-001`/`LETTA-001`'s own daemon wiring rather than factored into a shared `ExportTarget` interface; each connector's daemon-side code stays simple and self-contained.

## Design
Package: `internal/export/cognee` (invoked by `engine.ExportCognee`, daemon-side)

```go
type Client struct { /* base URL, optional bearer token */ }

func NewClient(baseURL, authTokenEnv string) *Client
func (c *Client) Add(ctx context.Context, datasetName string, content json.RawMessage) error // POST /api/v1/add
func (c *Client) Cognify(ctx context.Context, datasetName string, chunkSize int) error        // POST /api/v1/cognify
```

Each dependency's set of chunks is added as one payload to the project's dataset (dataset name = project ID), shaped as:

```json
{
  "dataset_name": "<project-id>",
  "data": [
    {"dependency": "google.golang.org/protobuf", "version": "v1.36.11", "ecosystem": "go", "content": "...", "source_type": "git", "trust_class": "repository"}
  ]
}
```

`cognify` is called once per export run, after every `add` call has completed, scoped to the same dataset name.

## Inputs / Outputs
- Input: `--project`, optional `--dependency`; Cognee REST base URL via config (`export.cognee.endpoint`) or flags, plus an optional bearer-token env var (`export.cognee.auth_token_env`) for a user-supplied auth layer — never assumed present, since the server has none of its own by default.
- Output: content added to the user's own Cognee dataset via `POST /api/v1/add`, `cognify` triggered via `POST /api/v1/cognify`; a text summary to stdout, same convention as the epic's other connectors.

## Failure behavior
- Cognee unreachable/misconfigured → clean, actionable pre-flight error before any content is added, same pattern as the epic's other connectors' `Health`-style check.
- One dependency's `add` failing does not abort the rest of the batch; `cognify` is still called once at the end over whatever was successfully added.

## Tests
- A fake HTTP server (`httptest`, matching the epic's other connectors' own test shape) verifying the exact `POST /api/v1/add` request body and the follow-up `POST /api/v1/cognify` call with the correct dataset name.
- A partial-failure test, same shape as `MEM0-001`'s.
- A test confirming the optional bearer token, when configured, is sent as an `Authorization` header — and that its absence doesn't break requests against a target with no auth (the real, disclosed default).
- A daemon-level test confirming `POST /v1/export/cognee` streams progress and a terminal summary, and that the CLI command never opens Badger itself.

## Acceptance criteria
- [x] `depctl export cognee --project <id>` adds every active-generation chunk for that project to a dataset named for the project ID, then triggers `cognify`, verified against a real fake-server request capture (`internal/cli/export_test.go`'s `TestExportCogneeAddsThenCognifies`).
- [x] `--dependency <name>` scopes to one dependency only (`api.ExportCogneeRequest.Dependencies`, filtered via the shared `nameSet` helper).
- [x] Partial failures during `add` are reported per-dependency, never abort the batch; `cognify` still runs over whatever succeeded — and is skipped entirely if nothing was added (`TestExportCogneeSkipsCognifyWhenNothingWasAdded`), since there'd be nothing for it to process.
- [x] README/`--help` text plainly discloses Cognee's own telemetry-by-default behavior, its open-by-default auth posture, and the known `cognify` telemetry-call stall (`topoteretes/cognee#2120`) (`depctl export cognee --help`'s `Long` text, `internal/cli/export.go`).

## Post-implementation note (2026-10-01)
Shipped per the epic's daemon-owned architecture decision, same shape as `MEM0-001`: `POST /v1/export/cognee` (`internal/daemon/api`, `server.go`, `handlers.go`), `engine.ExportCognee` (`internal/cli/daemon.go`), `Client.ExportCognee` (`internal/daemon/client/client.go`), `depctl export cognee` (`internal/cli/export.go`), and the `internal/export/cognee` HTTP client. One real correction made during implementation, not assumed from the original design: `POST /api/v1/add` is **multipart form-data** (a `data` file field plus a `datasetName` field), not JSON as originally sketched — confirmed directly against `docs.cognee.ai/guides/deploy-rest-api-server`'s own curl example. Each dependency's aggregated chunk set is uploaded as one JSON file per `add` call; `cognify` is called exactly once at the end, over the whole project dataset, only if at least one `add` succeeded. `Health()` uses the real `GET /api/v1/datasets` endpoint, since Cognee has no dedicated health check.

## Real-container verification (2026-10-01)
Verified against a real self-hosted `cognee/cognee:main` (v1.6.2) under Podman, configured fully local via Cognee's own documented Ollama settings (`LLM_PROVIDER=ollama`, `EMBEDDING_PROVIDER=ollama`, a placeholder `LLM_API_KEY=ollama`) — no paid API key needed. A real depctl project (`github.com/google/uuid` v1.6.0, really synced) was exported with the real `depctl` binary.

**One real bug found and fixed:** `POST /api/v1/cognify` is **fully synchronous** in current Cognee — the HTTP response doesn't return until the whole extraction pipeline finishes — contrary to both Cognee's docs and this ticket's original design ("returns once the request is accepted"). The first live run reported `cognify failed: context deadline exceeded` after the client's 30s timeout, while Cognee's own logs showed the pipeline actually completing successfully 2m41s later — a false failure. Fixed by removing the blanket `http.Client` timeout and bounding each call with its own context instead: `Add` 30s, `Health` 10s, `Cognify` 10 minutes (`internal/export/cognee/cognee.go`). The daemon's own `maxActionDuration` (30 min) and the CLI's `longRunningRequestTimeout` (35 min) already sit above that.

After the fix, against a fresh Cognee instance: `depctl export cognee` exited 0 after 159s; Cognee's dataset list showed one dataset named for the depctl project ID; and `POST /api/v1/search` for "How do I generate a new random UUID?" returned depctl's exported `NewRandom` doc chunk, carrying its version (`v1.6.0`), ecosystem, and `trust_class: repository` metadata. Re-exporting the same content returned in under a second — Cognee deduplicates already-processed content, so repeat exports are cheap.

Observation, not changed: each dependency is uploaded as one JSON document, which Cognee chunks as plain text — search hits return the JSON payload. It works, but uploading plain text or Markdown per chunk might give Cognee cleaner chunk boundaries. Worth revisiting only if real usage shows retrieval quality problems.

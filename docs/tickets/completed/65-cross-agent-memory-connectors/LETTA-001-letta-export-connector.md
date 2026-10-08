# LETTA-001: Letta export connector

**Epic:** Cross-Agent Memory System Connectors
**Status:** declined (2026-10-01)
**Depends on:** none (reads only already-shipped storage: `internal/data/badger`, `internal/query`)
**Estimated size:** medium

## Declined (2026-10-01) — target product discontinued
This ticket was built and shipped (`internal/export/letta`, `/v1/export/letta`, `depctl export letta`), then **removed** after real-container testing (Podman) found that Letta's REST archival-memory agent server — the thing this whole ticket targets — is a **retired product**, not just differently deployed. Concrete evidence, from primary sources, found during a live boot attempt:

- `letta/letta:latest` (the image this ticket's own research pointed at) no longer runs that server at all; its own startup message says plainly: *"The retired Python Letta server is end-of-life and this image now contains Letta Code"* — a completely different, unrelated coding-assistant CLI product.
- The current official self-hosting guide (`docs.letta.com/self-hosting/`) describes only Letta Code's "App Server" (channels, computer-use, OAuth device flow) — no archival memory, no stateful-agent-memory concept at all.
- `letta-ai/letta`'s own current README states it directly: *"The `archive` branch contains the retired Letta V1 API server... active projects should use the current source"* (Letta Code).

The REST docs this ticket was originally built against (`docs.letta.com/api-reference/archival-memory`, `docs.letta.com/guides/selfhosting`) are still published, but describe that retired V1 API — a real research miss, not caught until an actual boot attempt, that no amount of further doc-reading would have surfaced (the docs page itself gives no deprecation notice). This is categorically different from Mem0's telemetry wart or Graphiti's no-auth-by-default gap: those are live products with known rough edges; this is a product that no longer exists in deployable form. Declining per the same reasoning as epics 62/63 (Rust/Java resolvers) and 64 (Weaviate/Milvus/Chroma/pgvector adapters) — a real, evidence-based call, not a misunderstanding to revisit.

All shipped code (`internal/export/letta`, the `/v1/export/letta` daemon route, `depctl export letta`, and its tests) was removed from the codebase the same day — see `docs/architecture.md`'s dated entry.

## Goal
`depctl export letta --project <id> --agent <letta-agent-id> [--dependency <name>]` pushes a project's (or one dependency's) already-synced dependency knowledge into a user's own Letta agent's archival memory, so an agent built on Letta (formerly MemGPT) surfaces depctl's version-correct facts through its own archival-memory search, the same way `MEM0-001`/`GRAPHITI-001` do for Mem0/Graphiti.

## Non-goals
- No automatic/background export — explicit command only, same as `MEM0-001`/`GRAPHITI-001`.
- No import path (Letta → depctl).
- No agent creation or Postgres/model-provider management — Letta's self-hosted server requires its own Postgres backend and a model-provider API key; this connector assumes the user already has a running server and an existing agent ID, the same "bring your own instance" posture as `MEM0-001`/`GRAPHITI-001`.
- Not a dependency of depctl's core sync/query path — same isolation guarantee as the epic's other connectors.
- No attempt to manage Letta's own core-memory blocks or agent configuration — this connector only writes to archival memory, the store Letta's own docs describe as the right target for reference material an agent searches on demand (as opposed to core memory, which is always in-context).

## Transport — confirmed (2026-09-30), plain REST
Letta ships a real, self-hostable, Apache-2.0 server with the same REST surface as Letta Cloud (default `http://localhost:8283/v1`), distributed as the `letta/letta:latest` Docker image. Relevant endpoint (`docs.letta.com/api-reference`):

- `POST /v1/agents/{agent_id}/archival-memory` — insert a passage into an agent's archival memory. The confirmed request field is `text`; no separate structured-metadata field is documented, so this connector embeds depctl's own metadata (ecosystem, dependency, version, source type, trust class) as a labeled preamble line inside the text itself, matching the breadcrumb convention already used elsewhere in this codebase.
- `GET /v1/agents/{agent_id}/archival-memory`, `GET /v1/agents/{agent_id}/archival-memory/search` — read-side, not needed by this ticket.
- `DELETE /v1/agents/{agent_id}/archival-memory/{memory_id}` — destructive; this connector never calls it.

**A real, disclosed security fact, found during this verification**: the self-hosted server has **no authentication by default** (`docs.letta.com/guides/selfhosting`) — the same disclosed-gap shape as `GRAPHITI-001`. Production/untrusted deployments are documented as needing `SECURE=true` plus a `LETTA_SERVER_PASSWORD`. This connector's own docs must say so plainly and support an optional bearer-token config field, never assuming auth is present by default.

**Telemetry: unconfirmed, not disclosed as fact.** A `LETTA_CODE_TELEM` env var exists but documentation for it is scoped to the separate "Letta Code" CLI product, not confirmed to apply to the self-hosted agent server itself. Unlike Mem0's telemetry (confirmed via multiple open upstream issues), this connector's docs make no telemetry claim about Letta one way or the other.

**A caveat worth disclosing**: Letta's own GitHub README (`github.com/letta-ai/letta`) is Apache-2.0 licensed and does mention a `letta server` command for "local or self-hosted agents," but the README itself is clearly cloud-first — it foregrounds the desktop app, `chat.letta.com`, and Letta Cloud, and defers self-hosting entirely to a separate docs site rather than documenting it inline. The self-host facts above (Docker image, auth flags, endpoint shapes) come from that separate `docs.letta.com/guides/selfhosting` page, a real primary source — self-hosting is genuinely supported, just not the project's primary promoted path. Worth re-checking that doc page is still current immediately before implementation, since a cloud-first project is more likely to let its self-host docs drift.

**Scoping**: Letta archival memory is scoped per-agent, not per-project — every insert requires an existing `agent_id` the user supplies (`--agent` flag or `export.letta.agent_id` config), unlike Mem0's flat memory store or Graphiti's `group_id`. Letta separately has a real "Identity" concept (`identifier_key`) for associating external user/entity IDs with agents, but this connector does not need it — the user-supplied `agent_id` is the only scoping this ticket requires.

## Architecture — daemon-owned (ADR-011)
Same reasoning and wiring as `MEM0-001`'s own "Architecture" section: the CLI never opens Badger directly, this is a new daemon endpoint following the existing `/v1/sync` pattern.

- `api.PathExportLetta = "/v1/export/letta"`, `api.ExportLettaRequest`/`api.ExportLettaResponse` in `internal/daemon/api/api.go` — `ExportLettaRequest` carries the `AgentID` field alongside `ProjectID`/`Dependencies`.
- `Engine.ExportLetta(ctx, req) (*api.ExportLettaResponse, error)` added to the `Engine` interface (`internal/daemon/server.go`), implemented on `engine` in `internal/cli/daemon.go` — reads `e.badgerStore` directly, in-process, then inserts archival-memory passages into the user's Letta agent from inside that method.
- `mux.HandleFunc("POST "+api.PathExportLetta, s.handleExportLetta)` in `routes()`; `handleExportLetta` in `handlers.go` follows `handleSync`'s shape (streamed progress + terminal result).
- `Client.ExportLetta(ctx, req, out)` in `internal/daemon/client/client.go`, using `c.stream`.
- `depctl export letta` (`internal/cli/export.go`) is a thin CLI command: parse flags (including the required `--agent`), build `api.ExportLettaRequest`, call `Client.ExportLetta`, print the streamed progress/summary.

## Simplicity constraints
- New package `internal/export/letta` — a plain Go HTTP client against `POST /v1/agents/{agent_id}/archival-memory`, matching `MEM0-001`/`GRAPHITI-001`'s own shape (same package structure, same batching/pre-flight pattern) — no SDK dependency, no new transport primitive this codebase doesn't already have for HTTP. Used from inside `engine.ExportLetta`, not from the CLI process.
- Reads exclusively through existing storage APIs — same as `MEM0-001` (`ListGenerationChunks`, `GetKnowledgeObject`) — no new acquisition/chunking/embedding logic.
- Its own endpoint, request/response types, and `Engine` method — duplicated from `MEM0-001`/`GRAPHITI-001`/`COGNEE-001`'s own daemon wiring rather than factored into a shared `ExportTarget` interface; each connector's daemon-side code stays simple and self-contained.

## Design
Package: `internal/export/letta` (invoked by `engine.ExportLetta`, daemon-side)

```go
type Client struct { /* base URL, agent ID, optional bearer token */ }

func NewClient(baseURL, agentID, authTokenEnv string) *Client
func (c *Client) InsertArchivalMemory(ctx context.Context, text string) error // POST /v1/agents/{agent_id}/archival-memory
```

Each chunk is submitted as one archival memory passage, with metadata folded into the text body:

```
[depctl] dependency=google.golang.org/protobuf version=v1.36.11 ecosystem=go source_type=git trust_class=repository
<chunk content>
```

## Inputs / Outputs
- Input: `--project`, `--agent` (required — Letta has no default/global archival memory, only per-agent), optional `--dependency`; Letta REST base URL via config (`export.letta.endpoint`) or flags, plus an optional bearer-token env var (`export.letta.auth_token_env`) for a user-supplied auth layer — never assumed present, since the server has none of its own by default.
- Output: passages created in the user's own Letta agent's archival memory via `POST /v1/agents/{agent_id}/archival-memory`; a text summary to stdout, same convention as `MEM0-001`/`GRAPHITI-001`.

## Failure behavior
- Letta server unreachable, or the given `agent_id` does not exist → clean, actionable pre-flight error before any passage is attempted.
- One chunk's insert failing does not abort the rest of the batch.

## Tests
- A fake HTTP server (`httptest`, matching `MEM0-001`/`GRAPHITI-001`'s own test shape) verifying the exact `POST /v1/agents/{agent_id}/archival-memory` request body, including the metadata preamble format.
- A partial-failure test, same shape as `MEM0-001`'s.
- A test confirming the optional bearer token, when configured, is sent as an `Authorization` header — and that its absence doesn't break requests against a target with no auth (the real, disclosed default).
- A test confirming a missing/unknown `agent_id` produces a clean pre-flight error rather than a per-chunk failure spray.
- A daemon-level test confirming `POST /v1/export/letta` streams progress and a terminal summary, and that the CLI command never opens Badger itself.

## Acceptance criteria (as originally built — now moot, see Declined note above)
- [ ] ~~`depctl export letta --project <id> --agent <agent-id>` pushes one archival-memory passage per chunk via `POST /v1/agents/{agent_id}/archival-memory`~~ — built and verified against a fake server, then removed; the real target server doesn't exist.
- [ ] ~~`--dependency <name>` scopes to one dependency only~~ — same.
- [ ] ~~Partial failures are reported per-chunk, never abort the batch~~ — same.
- [ ] ~~README/`--help` text plainly discloses that self-hosted Letta has no built-in authentication by default~~ — same.

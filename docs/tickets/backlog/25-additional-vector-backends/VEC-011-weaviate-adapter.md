# VEC-011: Weaviate adapter

**Epic:** Vector Backends (bring-your-own + embedded)
**Status:** done (2026-10-05; revived 2026-10-01; declined 2026-09-30 — see the notes at the end)
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` for Weaviate (https://github.com/weaviate/weaviate, https://docs.weaviate.io/weaviate) using its HTTP API, passing the shared conformance suite.

## Non-goals
- Hybrid search tuning beyond exposing the `HybridSearch` capability flag if Weaviate supports it out of the box.
- GraphQL client generation — plain HTTP/REST is sufficient.

## Simplicity constraints
- Small hand-written HTTP client, matching the Qdrant adapter's approach (VEC-002 explicitly avoids requiring a generated client to keep dependency weight low) — do the same here rather than importing Weaviate's full Go client SDK unless it turns out meaningfully simpler.
- Map `Namespace` to a single Weaviate class per collection; do not build multi-tenant class-per-project logic.

## Design
Package: `internal/backend/weaviate`

```go
type Adapter struct {
    Endpoint string
    HTTPClient *http.Client
}
func (a *Adapter) Name() string { return "weaviate" }
func (a *Adapter) EnsureNamespace(ctx context.Context, ns backend.Namespace) error // create class if absent
func (a *Adapter) Upsert(ctx context.Context, req backend.UpsertRequest) error
func (a *Adapter) Delete(ctx context.Context, req backend.DeleteRequest) error
func (a *Adapter) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error)
func (a *Adapter) Health(ctx context.Context) error
func (a *Adapter) Capabilities(ctx context.Context) (backend.Capabilities, error)
```
Required metadata fields on every object per the namespace model (design spec §45): `ecosystem`, `dependency`, `version`, `generation`, `source_type`, `authority`. These map to Weaviate property filters for version-correct queries.

## Inputs / Outputs
Same as `VectorBackend` interface (VEC-001) — no new inputs/outputs beyond what the interface defines.

## Failure behavior
- Weaviate unreachable → `Health` returns error; upsert/query calls return typed backend errors, not silently swallowed.
- Schema/class creation conflict → idempotent `EnsureNamespace` (check-then-create, tolerate "already exists").

## Tests
- `conformance.RunConformanceSuite` via `testcontainers-go` Weaviate module.
- Insert two versions of the same package; filtered query returns only the requested version (per VEC-002's acceptance bar).

## Acceptance criteria
- [x] Passes the full VEC-010 conformance suite via testcontainers.
- [x] Metadata filters support package/ecosystem/version/generation per the namespace model.


## Done (2026-10-05)

`internal/backend/weaviate`: a small hand-written client with no SDK. It uses REST for schema, batch writes and batch deletes, and GraphQL for `nearVector` search and `Aggregate` counts, because Weaviate has no REST search. Selected with `vector.backend: weaviate`; `vector.api_key_env` names the env var holding a bearer API key.

Reconciled against the sketch above:
- **Collection name.** One Weaviate collection per namespace. ragctl's per-install namespace (`ragctl-<random>`) becomes `Ragctl_<random>`, because Weaviate names need a leading capital and no hyphens.
- **Object IDs** are a UUID derived from (generation, chunk ID), the same scheme as Qdrant, so a chunk shared by two versions is two objects. The chunk ID is stored as `chunk_id`, and delete-by-ID matches it with `ContainsAny`, which removes the chunk from every generation.
- **Exact-match filters.** Metadata text properties use `field` tokenization. With Weaviate's default `word` tokenization, `Equal "v1.5.0"` also matches `v1.0.5`. A new conformance subtest, `FilterMatchesWholeValuesNotTokens` (now 15), checks this for every backend, and switching to `word` tokenization fails it.
- **Paged deletes.** A batch delete is capped at the server's `QUERY_MAXIMUM_RESULTS` (10,000 by default), so deletes repeat until a pass comes in under the cap. A test runs Weaviate with a cap of 5 and checks that deleting 12 objects leaves 0; without the loop, 7 remain.
- **Errors Weaviate reports with HTTP 200.** The batch endpoint reports per-object failures (such as a wrong-sized vector), and GraphQL reports query errors; both are treated as failures. Weaviate batches aren't transactional, so an error can leave earlier objects written. The failed generation is never activated, and a rebuild rewrites them.
- **Health with an API key** reads `/v1/meta`. The readiness endpoint is unauthenticated and would report a wrong key as healthy. Filter values are JSON-escaped into GraphQL, and a test checks this with a version containing `"` and `\`.
- **Capabilities.** No keyword or hybrid search: ragctl stores no text in Weaviate.

Verified with the shared conformance suite and five Weaviate-specific tests against a real `weaviate:1.32.4` with API-key auth on. End to end, a sandboxed ragctl ran against a Weaviate that already held another app's collection. Results:
- sync and search worked;
- two projects on two versions each answered from their own version (84 + 81 objects);
- GC deleted exactly the unreferenced version;
- the other collection kept its object throughout;
- a wrong key shows in doctor as a clear 401 error.

This is [docs/demos/weaviate.md](../../../demos/weaviate.md), run as written. Docker Hub rate-limited unauthenticated pulls during the work; `mirror.gcr.io` serves the same image.

## Revived (2026-10-01)

The decline below judged this adapter as "another server to run," which was true for a user starting from nothing. It missed a different user: someone who **already runs** Weaviate (directly, or as the store under Weaviate-based agent memory tooling). For that user, Weaviate support means "ragctl uses what you already have": no Qdrant, no extra container. That is ragctl's positioning as of 2026-10-01 (see the README's "Vector store" section). If you already run a supported vector DB, ragctl uses it. If you don't, ragctl manages a local Qdrant for you, and `VEC-015` (embedded SQLite) is the longer-term no-service path. The supported set is deliberately Qdrant, Weaviate, and pgvector, which covers most existing deployments. Milvus and Chroma (`VEC-012`/`VEC-013`) stay declined.

Implementation notes for whoever picks this up:
- Use its own collection or class, never anything belonging to the user's other tooling. That's the same isolation rule the Qdrant adapter follows via its per-install collection name (`SAFE-001`).
- Verify against a real Weaviate under Podman, not only testcontainers fakes. Epic 65's real-container pass found bugs in three of its four connectors that fakes had passed.

## Declined (2026-09-30)

Not being built — a deliberate scoping decision, not a technical blocker. Backlog triage narrowed this epic to the two vector-backend tickets that actually matter for ragctl's own priorities right now: `VEC-010` (the shared conformance suite, backend-agnostic infrastructure) and `VEC-015` (embedded SQLite, a genuine zero-install alternative to Qdrant — see its own ticket). This adapter is the same weight class as Qdrant itself (a separate service to install and run) rather than a lighter alternative, so it does not address the actual friction ragctl's local-first users hit (needing Qdrant + a container runtime at all). Reusing `VEC-010`'s conformance suite, this remains a mechanical "write one more `VectorBackend` implementation" ticket if ever picked up later — no interface work needed.

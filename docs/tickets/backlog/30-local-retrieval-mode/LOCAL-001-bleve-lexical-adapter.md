# LOCAL-001: Bleve lexical adapter

**Epic:** Local-only retrieval mode
**Status:** planned
**Depends on:** VEC-001 (VectorBackend interface / Capabilities type)
**Estimated size:** medium

## Goal
Implement a `VectorBackend`-shaped keyword/BM25 retrieval adapter using Bleve, so `ragctl` can search locally without any external vector database.

## Non-goals
- No semantic/vector search from Bleve — lexical only. Bleve is not expected to match a real vector backend's relevance quality.
- No hybrid ranking logic — that belongs to whatever calls the backend, not this adapter.

## Simplicity constraints
- Bleve indexes live on local disk under `~/.local/share/ragctl/bleve/`; do not add a separate config surface beyond what other backends already use (endpoint/collection-equivalent = index path).
- Implement the minimum of the `VectorBackend` interface needed for lexical search — `EnsureNamespace`, `Upsert`, `Delete`, `Query`, `Health`, `Capabilities` — reusing existing generation-scoped metadata filtering (package/ecosystem/version/generation) rather than inventing a new filter model.

## Design
Package: `internal/backend/local` (or `internal/backend/bleve`)

Implements the `VectorBackend` interface (from VEC-001):

```go
type BleveBackend struct { /* index handle, path */ }

func (b *BleveBackend) Name() string
func (b *BleveBackend) Capabilities(ctx context.Context) (Capabilities, error) // {KeywordSearch: true, MetadataFilter: true, VectorSearch: false, ...}
func (b *BleveBackend) EnsureNamespace(ctx context.Context, ns Namespace) error
func (b *BleveBackend) Upsert(ctx context.Context, req UpsertRequest) error
func (b *BleveBackend) Delete(ctx context.Context, req DeleteRequest) error
func (b *BleveBackend) Query(ctx context.Context, req QueryRequest) (QueryResult, error)
func (b *BleveBackend) Health(ctx context.Context) error
```

`Upsert` indexes chunk text plus the same metadata fields (ecosystem/dependency/version/generation/source_type/authority) used by the Qdrant adapter, as Bleve document fields for filtering via Bleve's conjunction/term queries. `Query` combines a `bleve.NewMatchQuery` on text with `bleve.NewTermQuery` filters per metadata field.

## Inputs / Outputs
- Input: `UpsertRequest`/`QueryRequest` (same shapes as VEC-001).
- Output: `QueryResult` with lexical relevance scores.

## Failure behavior
Index corruption on open is a fatal `doctor`-visible error, not a silent empty index; `ragctl doctor` (OPS-002) should be extended to check Bleve index openability when this backend is configured.

## Tests
- Two versions of the same package indexed; metadata-filtered query returns only the requested version (mirrors VEC-002's acceptance test).
- Delete by filter removes only matching documents.
- Health check reports index open/closed correctly.

## Acceptance criteria
- [ ] `BleveBackend` implements the full `VectorBackend` interface.
- [ ] Generation/version metadata filtering works identically in spirit to the Qdrant adapter's conformance expectations.
- [ ] Unit tests cover upsert/query/delete/health.

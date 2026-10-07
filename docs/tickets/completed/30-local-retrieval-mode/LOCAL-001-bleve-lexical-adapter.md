# LOCAL-001: Keyword (BM25) adapter (shipped as plain Go, not Bleve)

**Epic:** Local-only retrieval mode
**Status:** done (2026-10-06)
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

**A real prerequisite gap, found while scoping this (2026-09-30), not yet fixed anywhere**: `backend.QueryRequest` (`internal/backend/backend.go`) has no field for the raw query string at all —

```go
type QueryRequest struct {
    Namespace string
    Vector    []float32
    TopK      int
    Filter    *Filter
}
```

— and the one real caller, `query.Service.search` (`internal/query/search.go`), unconditionally embeds the query text and passes *only* `vectors[0]` through:

```go
vectors, err := s.embedder.Embed(ctx, []string{text})
...
result, err := s.backend.Query(ctx, backend.QueryRequest{Vector: vectors[0], ...})
```

A pure-lexical backend has no way to receive the string it's supposed to search on. This needs a small interface change *before* `Query` can be implemented at all — add `Text string` to `QueryRequest`, and make `query.Service.search` check the configured backend's own `Capabilities()` before deciding whether to call the embedder: skip `s.embedder.Embed` entirely (never call it — not just discard its result) when `Capabilities().VectorSearch` is false, and always pass `Text: text` through regardless. This is genuinely small (one struct field, one capability check in one call site) but it's real scope this ticket's original sketch missed — call it out explicitly as part of this ticket's own work, not an assumed-already-done prerequisite.

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
- [x] `BleveBackend` implements the full `VectorBackend` interface. (Reconciled: `keyword.Store`, see below.)
- [x] Generation/version metadata filtering works identically in spirit to the Qdrant adapter's conformance expectations.
- [x] Unit tests cover upsert/query/delete/health.

## Done (2026-10-06)

`internal/backend/keyword`, a plain-Go BM25 keyword index in one bbolt file (`keyword.db`, next to `control.db`). It implements the full `VectorBackend` interface and passes the shared conformance suite (17 checks). The suite now gives every point text as well as a vector, so one set of expectations covers both kinds of backend.

Reconciled against the sketch above:
- **Plain Go instead of Bleve (the user's decision).** Bleve would have added about 30 modules and a second on-disk index format. As with the embedded vector store, ragctl scopes first and ranks second: every search is filtered to one dependency version (hundreds to a few thousand chunks), so BM25 is computed at query time over just those chunks, and no inverted index is kept. Keys are `ecosystem\0dependency\0version\0generation\0id`, so that filter is a prefix scan.
- **The tokenizer keeps identifiers intact.** `pgxpool.NewWithConfig()` yields `pgxpool.newwithconfig`, `pgxpool`, `newwithconfig`, `new`, `with`, `config`. camelCase, snake_case, kebab-case and module paths are split, and the whole identifier is kept as well. Stopwords are dropped only as plain words, never as identifier parts (`Client.Do` keeps `do`).
- **API docs lead with their qualified symbol.** Godoc chunks are indexed as `pgxpool.New` plus their content. The doc "New creates a new Pool" never spells out the name someone searches for. The signature is left out: its generic terms (`ctx`, `error`) lengthened every doc and cost two hits in the comparison below.
- **The prerequisite gap this ticket found is closed.** `backend.Point` and `backend.QueryRequest` carry `Text`. `query.Service`, `generation.Replicate` and validation's version-correctness check all work with no embedder.
- **Two conformance rules came from end-to-end runs:** deleting from, or counting in, a namespace that was never created is a no-op, or 0. In auto mode the vector store has no namespace until something is embedded, so GC and doctor failed there. Embedded, keyword, pgvector, Qdrant and Weaviate all failed the new checks until they were fixed.

**Measured against embedding search:** uuid v1.6.0 plus pgx v5.11.0, ten questions, a hit being the right doc in the top 3. Keyword search found 5 or 6 depending on tokenizer details, and `nomic-embed-text-v2-moe` vectors found 5.
- Both find exact names (`NewRandom`, `NewWithConfig`).
- Keyword wins when the question uses the docs' own words: "parse a UUID from a string" finds `Parse`/`MustParse`, and "get a connection from the pool" found `Acquire` in one variant.
- Vectors win on paraphrase: "run a function inside a transaction" finds `BeginFunc`.
- Both miss some: `NewV7` for "an id that sorts by creation time", and `MaxConns`.

With the qualified-symbol header, keyword search puts `pgxpool.New` first; without it, neither mode found it. Ten questions is a sanity check, not a benchmark: epic 26's evaluation framework is where retrieval quality gets measured properly.

### Update (2026-10-07): storage layout

The layout described above (one bucket keyed `ecosystem\0dependency\0version\0generation\0id`, plus a `(generation, id)` index) used about 4x the space of its data for the keyword index and 2.5x for vectors. Both stores now keep one bucket per generation, keyed by chunk ID, with the generation's ecosystem, dependency and version stored once, and pack pages full. v0.3.0 files are converted on first open. See `docs/architecture.md`, "Embedded and keyword storage layout", for the measurements.

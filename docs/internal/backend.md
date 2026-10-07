# internal/backend

`internal/backend` defines `VectorBackend`, the one narrow interface every ragctl search index implements, and the mandatory `PointMetadata` every stored point carries. There is deliberately no lowest-common-denominator query DSL — `Capabilities` just reports what an adapter can do.

Terms used below:

- **Point** — one chunk as stored in an index: the chunk ID, its metadata, and either a vector (vector stores) or its text (the keyword index).
- **Generation** — one build of one dependency version. Every point of a generation has the same ecosystem, dependency and version.
- **Namespace** — the collection/table/bucket an index writes into. ragctl uses one per install, named by `vector.collection` (e.g. `ragctl-1a2b3c4d`), and filters by metadata inside it — never one namespace per dependency version.

Implementations:

| Package | `Name()` | Search | Storage | Selected by |
|---|---|---|---|---|
| `internal/backend/embedded` | `embedded` | exact cosine over the filtered points | bbolt file `vectors.db` next to `control.db` (or `vector.endpoint` if set) | `vector.backend: embedded` — the default for fresh installs |
| `internal/backend/qdrant` | `qdrant` | Qdrant HTTP API | Qdrant server (ragctl-managed container via `ragctl init --vector-backend qdrant`, or your own) | `vector.backend: qdrant` |
| `internal/backend/pgvector` | `pgvector` | `pgvector` HNSW index, cosine | one Postgres table per namespace | `vector.backend: pgvector` |
| `internal/backend/weaviate` | `weaviate` | Weaviate GraphQL `nearVector` | one Weaviate collection ("class") per namespace | `vector.backend: weaviate` |
| `internal/backend/keyword` | `keyword` | BM25 over the filtered points | bbolt file `keyword.db` next to `control.db` | written whenever `retrieval.mode` is `auto` or `keyword` |
| `internal/backend/backendtest` | `fake` | in-memory cosine | none | tests only |

The vector store is built by `buildVectorBackend` (internal/cli/pipeline.go). `vector.api_key_env` names the environment variable holding Qdrant's or Weaviate's API key, or the Postgres password; it is read by the process that builds the backend (normally the daemon). The keyword index is built by `buildSearchIndex` (internal/cli/retrieval.go), which wraps whichever indexes the retrieval mode writes in one `searchIndex` — itself a `VectorBackend` — so sync, validation, promotion, GC and search code never need to know which indexes exist. `searchIndex.Name()` is always `vector.backend`, so it is the active-generation key in every mode (see [internal/cli](cli.md)).

## Key types and functions

**internal/backend** (internal/backend/backend.go)
- `VectorBackend` — `Name()`, `Capabilities(ctx)`, `EnsureNamespace(ctx, ns)`, `Upsert(ctx, req)`, `Delete(ctx, req)`, `Query(ctx, req)`, `Count(ctx, namespace, filter)`, `Health(ctx)`. `Count` is exact (not TopK-bounded); doctor uses it to spot an ACTIVE generation with no points.
- `Capabilities` — `VectorSearch`, `KeywordSearch`, `HybridSearch`, `MetadataFilter`, `DeleteByFilter`. Every vector store reports `VectorSearch`, `MetadataFilter`, `DeleteByFilter`; the keyword index reports `KeywordSearch` instead of `VectorSearch`. Nothing reports `HybridSearch`.
- `Namespace` — `Name`, `Dimensions`, `Distance`. Dimensions come from the embedder; the keyword index ignores both. The embedded, pgvector and Weaviate stores accept only cosine.
- `PointMetadata` — `Ecosystem`, `Dependency`, `Version`, `Generation`, `SourceType`, `Authority`: a fixed set, not a key/value bag.
- `Point` — `ID` (the chunk ID), `Vector` (nil when no embedder was used), `Text` (what the keyword index indexes; vector stores ignore it), `Metadata`. A point's identity is **(generation, chunk ID)**: chunk IDs are content-derived, so two generations routinely share one, and each must keep its own point. Deleting by ID removes that chunk from every generation.
- `Filter` — `Ecosystem`, `Dependency`, `Version`, `Generation`; a point matches when every non-empty field equals its metadata **as a whole value** (`v1.0` does not match `v1.0.5`).
- `UpsertRequest`, `DeleteRequest` (IDs, Filter, or both as a union), `QueryRequest` (`Vector` for a vector store, `Text` for the keyword index, `TopK`, `Filter`), `ScoredPoint`, `QueryResult`. Scores are always "higher is better" (cosine similarity, or BM25).

**internal/backend/conformance** (conformance.go)
- `Run(t, newBackend)` — the shared contract test every implementation's own test calls (17 subtests). Each subtest gets a fresh backend and a random namespace, so one server can back the whole run. Covered: health; capabilities (some search kind plus `MetadataFilter`); idempotent `EnsureNamespace`; IDs and metadata round-trip unchanged; ranking and `TopK`; re-upsert doesn't duplicate; version, generation and whole-value filtering; the same ID in two generations is two points, and delete-by-ID removes both; exact `Count`; delete by IDs, by filter (every field must match, not any), and both as a union; delete and count in a namespace that was never created are a no-op and 0; namespace isolation.
- Test points carry both a vector and a `Text` built from it (`textFor`: each dimension is a word repeated in proportion to its weight), so one set of expectations covers vector and keyword backends. Delete-by-filter subtests skip when `DeleteByFilter` is false.
- Qdrant, pgvector and Weaviate run it against a container (skipped when no container runtime is reachable); embedded, keyword and the fake run it directly.

**internal/backend/embedded** (embedded.go, migrate.go) — see [Embedded and keyword stores](#embedded-and-keyword-stores)
- `Store`, `New(path)` — one bbolt file; one open handle per path per process, shared by every `Store` for that path. Opening waits at most 2s for bbolt's file lock and then reports the file as in use by another ragctl process (normally the daemon).
- `ErrDimensionMismatch` — the namespace exists with a different dimension (e.g. the embedding model changed), or a point/query vector has the wrong length.
- `Upsert` writes the whole request in one transaction. `Query` scores every point in the generations the filter selects and returns an error if the namespace doesn't exist.

**internal/backend/keyword** (keyword.go, tokenize.go, migrate.go)
- `Store`, `New(path)` — same file handling as `embedded`.
- `Upsert` stores each point's tokenized `Text` as term counts plus document length; `Query` ranks by BM25 (k1 = 1.2, b = 0.75) with document frequencies computed over only the points the filter selects, so the corpus is the dependency version being searched. Points matching no query term aren't returned; a query with no searchable terms returns the selected points unranked (score 0). A namespace that doesn't exist yet returns no matches.
- `Tokenize(text)` — keeps programming identifiers intact as well as split: `pgxpool.NewWithConfig()` yields `pgxpool.newwithconfig`, `pgxpool`, `newwithconfig`, `new`, `with`, `config`. Lowercases everything, drops plain English stopwords (never identifier parts), and drops tokens shorter than 2 or longer than 64 characters.
- The text it indexes comes from `generation.keywordText` (internal/data/generation/replicate.go): the chunk content, led by the qualified symbol (`package.Symbol`) for API-doc chunks, since a doc comment rarely spells out the name people search for.

**internal/backend/qdrant** (qdrant.go, types.go, errors.go)
- `Client`, `New(endpoint, opts...)`, `WithBatchSize` (default 256 points per upsert request), `WithAPIKey` (sent as the `api-key` header), `WithHTTPClient`. Hand-written `net/http` client, no SDK; 60s per-request timeout.
- `pointID(generationID, chunkID)` — Qdrant only accepts unsigned integers or UUIDs as point IDs, so the ID is a UUIDv5-shaped blake3 hash of generation + chunk ID. The chunk ID itself is stored in the payload under `_id` and returned by `Query`.
- `Delete` — deletes each chunk ID via an `_id` payload filter (removing it from every generation), then the `Filter` as a separate request; a collection that doesn't exist is a no-op. `Count` uses `points/count` with `exact: true`.
- `Health` — `/healthz`, or `/collections` when an API key is set (Qdrant serves `/healthz` without auth, so a wrong key would otherwise look healthy). `HealthCollection` checks one collection; nothing calls it yet.
- `Error`, `ErrBackendUnavailable` (5xx or connection failure, retryable), `ErrBackendRequest` (4xx or malformed, not retryable).

**internal/backend/pgvector** (pgvector.go)
- `Adapter`, `New(dsn, password)` — one connection pool per DSN+password per process; `password`, when set, overrides any in the DSN. No connection until first use.
- `EnsureNamespace` creates the `vector` extension only if it's missing (shared databases often don't grant that; `ErrExtensionMissing` otherwise), then a table with primary key `(generation, id)`, indexes on `id`, `(dependency, version)`, `generation`, and an HNSW cosine index. An existing table with a different vector size is `ErrDimensionMismatch`.
- `Upsert` is one transaction; `Delete`/`Count` on a table that doesn't exist are a no-op / 0. Score is `1 - cosine distance`.

**internal/backend/weaviate** (weaviate.go)
- `Client`, `New(endpoint, apiKey)` — REST for schema, writes and deletes; GraphQL for search and counts. The API key is sent as a bearer token; with a key set, `Health` reads `/v1/meta` because the readiness endpoint is unauthenticated.
- Namespaces map to class names (`ragctl-1a2b3c4d` → `Ragctl_1a2b3c4d`). Text properties use `field` tokenization so filters match whole values.
- Object IDs use the same (generation, chunk ID) UUID scheme as Qdrant. Batch writes aren't transactional: on error, earlier objects in the request may already be written. Batch deletes repeat until a pass deletes fewer than Weaviate's per-request cap.

**internal/backend/backendtest** (fake.go)
- `Backend`, `New()` — in-memory fake keyed by (generation, chunk ID), cosine search, `Health` always succeeds. Used by tests across the codebase.

## Embedded and keyword stores

Both bbolt-backed stores scope first and rank second: every ragctl search is filtered to one dependency version (hundreds to a few thousand chunks), so they compute exact scores over just those points at query time and keep no approximate or inverted index.

**Layout.** Each namespace is a top-level bucket holding:

```
<namespace>/
  dims                      (embedded only) uint32 vector dimension
  generations/              generation ID -> "ecosystem\0dependency\0version"
  chunks/
    <generation ID>/        one bucket per generation
      <chunk ID> -> value
```

- A generation is one dependency version, so ecosystem/dependency/version are stored once per generation, not per point. An upsert whose metadata contradicts its generation's stored values is an error, not a silent relabel.
- A point's identity (generation, chunk ID) is its location, so no separate ID index is needed. A filter naming a generation is a direct bucket lookup; otherwise only the small `generations` bucket is scanned. Deleting by filter drops whole generation buckets.
- Each upsert is sorted by (generation, chunk ID), and chunks arrive in key order, so a generation's bucket only grows at the end. Buckets are therefore filled to 100% (`FillPercent = 1.0`) instead of bbolt's default half-full page split.
- Values: embedded stores source type, authority and the vector as little-endian binary (4 bytes per dimension). Keyword stores source type, authority, document length and each distinct term with its count as varints.

**v0.3.0 conversion.** v0.3.0 used one `points` bucket per namespace keyed `ecosystem\0dependency\0version\0generation\0id`, plus an `ids` identity index — about 2.5x (embedded) and 4x (keyword) the space of the current layout. On first open, `migrateIfNeeded` (migrate.go in each package) detects a `points` bucket and converts the whole file:

1. Stream every namespace into `<file>.migrating` in the current layout, in transactions of at most 2,000 (embedded) or 10,000 (keyword) points. Embedded copies values byte for byte (no vector is recomputed); keyword re-encodes v0.3.0's fixed-width term counts as varints.
2. Close both files and rename the new one over the original, which also compacts it.

The original file is untouched until the new one is complete, so a failure or crash midway loses nothing; the next open simply starts the conversion again.

## Dataflow

```mermaid
flowchart TD
    Gen["internal/data/generation\nReplicate / AddToIndex"] -->|Upsert points\n(vector and/or Text)| SI["cli.searchIndex\n(one VectorBackend)"]
    Query["internal/query.Service"] -->|Query with Filter| SI
    Validate["internal/lifecycle/validate"] -->|Query filtered to the generation| SI
    GC["internal/lifecycle/gc"] -->|Delete by Filter| SI
    Doctor["ragctl doctor / status"] -->|Count, Health| SI
    SI -->|auto, keyword modes| KW["keyword.Store\n(keyword.db)"]
    SI -->|auto, vector modes| VS["vector store\n(vector.backend)"]
    VS --- EM["embedded.Store (vectors.db)"]
    VS --- QD["qdrant.Client"] --> QS[(Qdrant)]
    VS --- PG["pgvector.Adapter"] --> PS[(Postgres)]
    VS --- WV["weaviate.Client"] --> WS[(Weaviate)]
```

Writes and deletes go to every index in the `searchIndex`. A query uses the vector store when the request carries a vector and falls back to keyword search when it doesn't, or when the vector search finds nothing (a generation built while the embedder was unavailable has no vectors). `Count` is the largest count across the indexes.

## Walkthrough

Scenario: a fresh install (`vector.backend: embedded`, `retrieval.mode: auto`, Ollama running) syncs `github.com/jackc/pgx/v5@v5.7.1`. Generation `gen_01JA…` has 300 chunks; `generation.Replicate` upserts them in batches of 64.

1. `Replicate` calls `EnsureNamespace(ctx, Namespace{Name: "ragctl-1a2b3c4d", Dimensions: 768, Distance: "cosine"})` on the `searchIndex`, which calls it on both stores. `embedded.Store` creates the namespace bucket, records `dims = 768`, and creates empty `generations` and `chunks` buckets; `keyword.Store` creates the same minus `dims`.

2. For the first batch, `embedBatch` builds 64 `backend.Point`s, e.g.:

   ```go
   backend.Point{
       ID:     "chk_q3m7…",
       Vector: []float32{0.0123, -0.0456, /* …768 */},
       Text:   "pgxpool.NewWithConfig\nNewWithConfig creates a new Pool. config must have been created by ParseConfig.",
       Metadata: backend.PointMetadata{
           Ecosystem: "go", Dependency: "github.com/jackc/pgx/v5", Version: "v5.7.1",
           Generation: "gen_01JA…", SourceType: "git", Authority: 100,
       },
   }
   ```

3. `embedded.Store.Upsert` sorts the batch by (generation, chunk ID) and, in one transaction, checks each vector has 768 dimensions. The first point of the generation stores `"go\0github.com/jackc/pgx/v5\0v5.7.1"` under `generations/gen_01JA…` and creates bucket `chunks/gen_01JA…`; every point is then written under its chunk ID. `keyword.Store.Upsert` does the same with `Tokenize(Text)`'s term counts as the value.

4. Later, an agent searches pgx for "configure pool max connections". `query.Service` embeds the text and sends `QueryRequest{Vector: …, Text: "configure pool max connections", TopK: 10, Filter: {Ecosystem: "go", Dependency: "github.com/jackc/pgx/v5", Version: "v5.7.1", Generation: "gen_01JA…"}}`. The `searchIndex` sends it to `embedded.Store.Query`: the filter names a generation, so it opens `chunks/gen_01JA…` directly, scores all 300 vectors by cosine similarity, and returns the top 10 with metadata rebuilt from the generation record.

5. When retention later collects v5.7.1, `gc.Run` sends `Delete` with `Filter{Ecosystem: "go", Dependency: "github.com/jackc/pgx/v5", Version: "v5.7.1"}`. Each store scans `generations`, finds `gen_01JA…` matches, and drops its chunk bucket and its `generations` entry.

## Notes

- `Capabilities` is advertised, not enforced: an adapter with `DeleteByFilter == false` must reject a non-nil `Filter` itself; the interface doesn't check. Every current adapter supports delete-by-filter.
- Filters must match whole values. Weaviate's default word tokenization would match `v1.5.0` against `v1.0.5`, which is why its text properties use `field` tokenization; the conformance suite checks this for every backend.
- Point identity is (generation, chunk ID) because content reuse gives an unchanged chunk the same ID in every version that has it. A backend keyed on chunk ID alone lets one version's upsert overwrite another's point, silently emptying the older version's search — found end to end on pgvector before the conformance check existed.
- Deleting or counting in a namespace that was never created must succeed (no-op / 0): in auto mode the vector store has no namespace until something is embedded, yet GC and doctor still touch every index.
- Qdrant's `points/delete` takes "one of {points, filter}": sending both in one body silently deleted only by ID (verified against a live v1.13.1 server), so IDs and filter always go in separate requests.
- The embedded and keyword stores hold bbolt's file lock for as long as the process keeps them open, so normally only the daemon has them open; another process gets the "in use by another ragctl process" error after 2s.

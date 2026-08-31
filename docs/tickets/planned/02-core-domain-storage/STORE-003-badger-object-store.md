# STORE-003: Build Badger object store

**Epic:** Core Domain and Storage
**Status:** planned
**Depends on:** CORE-001
**Estimated size:** large

## Goal
Implement the BadgerDB-backed data-plane store for high-volume content: normalized knowledge objects, chunks, generation manifests, and embedding metadata.

## Non-goals
- No control-plane state (project registry, active pointers, jobs — that's STORE-001/bbolt).
- No embedding vectors here for the vector-backend path (vectors go to the configured `VectorBackend`; this store only holds embedding *metadata* for caching/reuse, see EMB-003 later).

## Simplicity constraints
- Wrap `*badger.DB` directly behind a small `Store` struct with explicit methods per keyspace — no generic key-value abstraction layer, no ORM.
- Do not implement Badger value-log GC scheduling in this ticket (see the later maintenance-jobs ticket) — just expose the store.

## Design
Package: `internal/data/badger`

```go
type Store struct { db *badger.DB }

func Open(path string) (*Store, error)
func (s *Store) Close() error

func (s *Store) PutKnowledgeObject(ctx context.Context, obj domain.KnowledgeObject) error
func (s *Store) GetKnowledgeObject(ctx context.Context, id string) (domain.KnowledgeObject, error)
func (s *Store) DeleteKnowledgeObject(ctx context.Context, id string) error

func (s *Store) PutChunk(ctx context.Context, generationID string, c domain.Chunk) error
func (s *Store) GetChunk(ctx context.Context, generationID, chunkID string) (domain.Chunk, error)
func (s *Store) ListGenerationChunks(ctx context.Context, generationID string) ([]domain.Chunk, error)

func (s *Store) PutManifest(ctx context.Context, generationID string, manifest []byte) error
func (s *Store) GetManifest(ctx context.Context, generationID string) ([]byte, error)

func (s *Store) PutEmbeddingMetadata(ctx context.Context, key string, meta []byte) error
func (s *Store) GetEmbeddingMetadata(ctx context.Context, key string) ([]byte, error)

func (s *Store) DeleteGeneration(ctx context.Context, generationID string) error // deletes chunk/manifest/embed keys for the generation, batched
```

Keyspace prefixes (byte string keys):
```
obj/<object-id>
chunk/<generation-id>/<chunk-id>
manifest/<generation-id>
embed/<generation-id>/<provider>/<chunk-id>       # per-replica embedding metadata, generation-scoped
embedcache/<content-hash>/<provider>/<model-id>   # content-addressed embedding cache, see EMB-003 — survives across generations on purpose
source-meta/<snapshot-id>
stage/<job-id>/...
hash/<content-hash>                                # object dedup index, see GEN-003
```
Use `db.NewWriteBatch()` for multi-key writes/deletes (e.g. `DeleteGeneration`) rather than looping single-key transactions.

Values are JSON-encoded domain structs for v1 (debuggability first; a compact binary encoding can be introduced later if profiling justifies it — do not do it now).

Iterators (`ListGenerationChunks`, `DeleteGeneration`) must always call `it.Close()` via `defer` and use `badger.IteratorOptions.PrefetchValues` deliberately (off for key-only scans).

## Inputs / Outputs
- Input: domain objects (`KnowledgeObject`, `Chunk`), raw manifest/metadata bytes.
- Output: persisted content on disk; readback via getters and prefix-scoped list.

## Failure behavior
- Not-found lookups return `ErrNotFound` (sentinel, `errors.Is` compatible).
- `Open` on a locked/corrupt directory returns a wrapped error.

## Tests
- Restart persistence: write objects/chunks, `Close()`, reopen at the same path, verify content readable.
- Batch write test: `PutChunk` for N chunks in one generation, verify `ListGenerationChunks` returns all N.
- Generation delete test: after `DeleteGeneration`, `ListGenerationChunks` for that generation returns empty and `GetManifest` returns `ErrNotFound`.
- Iterator resource test: run `ListGenerationChunks` in a loop (e.g. 100x) under `-race` and confirm no goroutine/fd leak (can pair with `goleak` if already wired in; otherwise assert no error from repeated calls).

## Acceptance criteria
- [x] Restart persistence test passes.
- [x] Batch write test passes.
- [x] Generation delete test passes.
- [x] Iterators do not leak resources (all `Close()`d, verified by test).

## Post-implementation note
The keyspace list named `hash/<content-hash>` (the object dedup index) but the Design block's method list didn't name explicit getter/setter methods for it. Added `PutContentHashIndex`/`GetContentHashIndex` (`internal/data/badger/hashindex.go`) for GEN-003 to use — same `Get`/`Put`-on-a-prefixed-key shape as every other keyspace here, no new abstraction.

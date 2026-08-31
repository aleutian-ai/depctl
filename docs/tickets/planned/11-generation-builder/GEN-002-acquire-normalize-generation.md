# GEN-002: Acquire/normalize generation pipeline

**Epic:** Generation Builder
**Status:** planned
**Depends on:** GEN-001, GIT-002, NORM-002, NORM-004
**Estimated size:** medium

## Goal
Drive a `PLANNED` generation through source acquisition, normalization, fingerprinting, and chunking into Badger staging, updating generation state at each stage (`ACQUIRING` → `NORMALIZING` → `INDEXING`).

## Non-goals
- Embedding generation and vector backend replication are separate milestones (EMB-*, VEC-*) — `INDEXING` here only means "chunks staged in Badger," not "embedded and replicated."
- No parallelism/worker-pool infrastructure yet; a straight sequential pipeline call is enough for v0.1.

## Simplicity constraints
- Implement this as one linear function, not a generic pluggable "pipeline framework." A pipeline abstraction is not justified until a second, meaningfully different pipeline exists.
- Do not add retry/backoff logic inside this ticket — GEN-002 assumes GIT-002/NORM-* already fail cleanly; retries belong to the job system (later milestone) if introduced at all for v0.1.

## Design
- Package: `internal/data/generation` (same package as GEN-001), function `func Build(ctx context.Context, gen domain.Generation, sources []domain.KnowledgeSource, store *bbolt.Store, badger *badger.Store) error`.
- Steps, updating `Generation.State` in bbolt before each phase:
  1. `ACQUIRING`: for each `KnowledgeSource`, call the Git acquisition layer (GIT-002) to materialize a `SourceSnapshot` (worktree checkout).
  2. `NORMALIZING`: run the matching `Normalizer` (NORM-002 Markdown, NORM-004 Go doc, selected via `Normalizer.Supports(snapshot)`) to produce `[]domain.KnowledgeObject`.
  3. `INDEXING`: chunk each object (via Chunker, from milestone 9) and write objects + chunks to Badger under `obj/<object-id>` and `chunk/<generation-id>/<chunk-id>`.
- Update the Badger manifest's `sources`, `object_count`, `chunk_count` as work completes.
- On any stage failure, set `Generation.State = FAILED` with an error message field and return.

## Inputs / Outputs
- Input: a `PLANNED` generation plus its knowledge sources (from registry match, REG-003).
- Output: generation reaches `INDEXING` complete with objects/chunks staged in Badger, or `FAILED`.

## Failure behavior
- Any normalizer/acquisition error aborts the whole generation build and marks it `FAILED` — no partial generation is left in an ambiguous state (still visible as staging, never promoted).
- Errors are typed (`ErrAcquisition`, `ErrNormalization`) so callers/tests can assert on failure category.

## Tests
- End-to-end: one Markdown source + one Go source → generation reaches `INDEXING` with expected object/chunk counts.
- Acquisition failure (bad Git ref) → generation `FAILED`, no chunks written.
- Normalizer error on one file does not silently drop the whole source — decide and test: fail whole generation for v0.1 (simplest, matches "deterministic behavior" principle).

## Acceptance criteria
- [x] State transitions `PLANNED → ACQUIRING → NORMALIZING → INDEXING` are observable via `GetGeneration` during a build.
- [x] Failure at any stage leaves generation in `FAILED` with a stored error message.

## Post-implementation note
`Build`'s `sources` parameter is `[]registry.Source` (REG-001's manifest shape — `ID`/`Type`/`URL`/`Ref`/`Module`/`Authority`), not `[]domain.KnowledgeSource` as sketched here: `domain.KnowledgeSource` was never actually built (only sketched as a comment in CORE-001), and `registry.Match` already returns exactly the shape a generation build needs. Adding a second, redundant type just to match the ticket's literal signature would have meant a translation step with no consumer benefit. Only `type: "git"` sources are acquired (via GIT-001/002) — a `type: "godoc"` manifest entry is informational for v0.1: it's naturally covered by walking the same worktree a `git` source already materialized and finding `.go` files there, not a separate acquisition. Non-`git`/`godoc` source types (e.g. `website`) are out of scope until their own acquisition milestone exists, per GEN-002's own dependency list (GIT-002, NORM-002, NORM-004 only).

A source's `Ref` template (e.g. `"v${version}"`) has `${version}` substituted with the dependency version stripped of any leading `v` before resolution, so both Go's `v1.2.3` and a bare-version ecosystem's `1.2.3` land on the same tag shape — this substitution rule wasn't specified anywhere in REG-001/GIT-002/GEN-002 and had to be decided here; documented in `internal/data/generation/build.go`'s `gitRef`.

# GEN-001: Generation creator

**Epic:** Generation Builder
**Status:** planned
**Depends on:** STORE-001, STORE-003, HASH-002
**Estimated size:** small

## Goal
Create a staging `Generation` record in bbolt and its corresponding manifest in Badger whenever a dependency version needs new/updated knowledge, so later pipeline stages have a target to write into.

## Non-goals
- Does not acquire, normalize, or embed content (GEN-002).
- Does not decide *which* dependency versions need a generation (that's the planner, PLAN-001).

## Simplicity constraints
- One generation = one dependency version's knowledge snapshot. Do not try to batch multiple dependencies into one generation record.
- Generation ID: use ULID (`github.com/oklog/ulid/v2`) prefixed `gen_`. Do not derive it from content hashes — generations are lifecycle entities, not content-addressed.
- No background scheduling logic here — this is a synchronous constructor called by the planner/sync command.

## Design
- Package: `internal/data/generation`.
- `func Create(ctx context.Context, store *bbolt.Store, badger *badger.Store, dep domain.DependencyVersion) (domain.Generation, error)`.
- `domain.Generation` fields: ID, DependencyVersion, State (starts `PLANNED`), CreatedAt, UpdatedAt.
- bbolt: write to `generations` bucket, key `generations/<generation-id>`.
- Badger: write initial manifest skeleton to `manifest/<generation-id>` (object_count=0, chunk_count=0, sources=[]) per the manifest JSON shape in the design spec (id, dependency, sources, object_count, chunk_count, embedding_model, created_at).
- Both writes happen outside a single cross-store transaction (bbolt and Badger are separate engines); write Badger manifest first, then bbolt pointer, so a crash never leaves a bbolt record with no manifest.

## Inputs / Outputs
- Input: a resolved `domain.DependencyVersion`.
- Output: a `domain.Generation` in state `PLANNED`, with an empty manifest staged in Badger.

## Failure behavior
- If Badger write fails, return error and do not write bbolt record.
- If bbolt write fails after Badger succeeded, the orphaned manifest is harmless (unreferenced, eventually GC-able) — log a warning.

## Tests
- Creating a generation persists a `PLANNED` record retrievable via `GetGeneration`.
- Manifest exists in Badger at expected key immediately after creation.
- Two generations for the same dependency version get distinct IDs.

## Acceptance criteria
- [ ] `ragctl` internal API can create a generation and it appears as `PLANNED` via `GetGeneration`.
- [ ] Manifest skeleton readable from Badger right after creation.

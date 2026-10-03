# STORE-005: Per-version active-generation pointers

**Epic:** Version-correct active generations
**Status:** done (2026-10-02)
**Depends on:** ADR-012
**Estimated size:** medium

## Goal
Key the control store's active-generation pointer by `(ecosystem, dependency, version, backend)` instead of `(ecosystem, dependency, backend)`, so several versions of one dependency can be active at once (ADR-012).

## Non-goals
- No change to retention/GC rules: version lifetime stays owned by references and grace periods (epic 16).
- No version-less convenience lookup. Callers that want every version list pointers.

## Simplicity constraints
- Same bucket, new key shape. No new bucket for pointers.
- Migration runs once, inside `bbolt.Open`, in one transaction; it's idempotent.

## Design
`internal/control/bbolt/active_generations.go`:
- `activeGenerationKey(ecosystem, dependency, version, backend)` → `ecosystem|dependency|version|backend`.
- `PromoteGeneration(ctx, candidate, backend)`: keys by `candidate.Dependency.Version`; supersedes only the prior active generation for that same version.
- `ClearActiveGeneration(ctx, ecosystem, dependency, version, backend)`.
- `GetActiveGeneration(ctx, ecosystem, dependency, version, backend)`.
- `ListActivePointers(ctx, backend)`: `ActivePointer` gains a `Version` field.
- Migration: any key with exactly three `|`-separated parts is old-format; read its generation record, rewrite under the four-part key, delete the old key. A pointer whose generation record is missing is kept as-is, so `doctor` can still report it. A dependency name never contains `|`, so the part count is unambiguous.

## Tests
- Two versions of one dependency promoted: both active; promoting a rebuild of one supersedes only that version's prior generation.
- `ClearActiveGeneration` for one version leaves the other active.
- Migration: an old-format pointer is rewritten and resolves by version; running it twice is a no-op; a dangling old pointer is kept.

## Post-implementation note (2026-10-02)
Shipped as designed: `internal/control/bbolt/active_generations.go` keys pointers `ecosystem|dependency|version|backend`, and `GetActiveGeneration`/`ClearActiveGeneration` take the exact version. No version-less variant exists. The migration is control-store schema version 2 (`schema.go`'s existing migration mechanism); a pointer whose generation record is missing is kept and listed with an empty version so `doctor` still sees it. All 15 callers were updated. Where a caller legitimately wants "the newest across versions" (VAL-002's sanity baseline, `describe`), it uses `latestActiveGenerationAnyVersion` (`internal/cli/sync.go`), named so it can't be mistaken for "is this version built?".

**Verified on real data:** an old binary (pre-ADR-012) synced a project, then the new binary opened the same store. It migrated to schema 2, kept the same generation active, searches returned it unchanged, and a resync rebuilt nothing.

**Real finding from the acceptance run, fixed:** GC could no longer remove an unreferenced version. `PlanGC` treated an active version as never eligible; that only worked under the old model because promoting a newer version made the old one inactive. Now references alone decide lifetime (ADR-012 updated), and `gc.Run` clears a version's active pointer as its first deletion step. Verified live: dropping project A's dependency let GC remove v1.5.0's 81 points and retire its pointer, while v1.6.0 was untouched.

**Also changed, same reasoning:** project search filters by the active generation's ID, not just the version, so a rebuild's leftover predecessor never mixes stale chunks into results. Seen live: during the acceptance run, v1.4.0 briefly had two generations' points (150), and search returned only the active one.

- [x] Pointers keyed by version; the old version-less signatures are gone.
- [x] Existing installs migrate on open: unit-tested with seeded old-format keys, and verified on a real old-binary database.
- [x] Every caller compiles against the version-explicit API.

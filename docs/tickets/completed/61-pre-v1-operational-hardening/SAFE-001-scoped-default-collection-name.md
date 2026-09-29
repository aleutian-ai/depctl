# SAFE-001: Guard against ambient sync into a shared/foreign Qdrant collection

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done — 2026-09-28
**Depends on:** none
**Estimated size:** small

## Problem, confirmed live (not hypothetical)
`internal/config/config.go`'s `Default(dataDir)` sets `Vector.Collection: "ragctl"` — a plain, unscoped literal, identical for every installation. `SyncConfig.DisableAmbient` defaults to `false`: `ragctl scan` registering a new project automatically triggers a full ambient sync (epic 54/SCOPE-002) with no separate opt-in step.

Combine the two: any isolated/test/second `ragctl` instance pointed at the same Qdrant server that doesn't *explicitly* override `vector.collection` before its first `scan` will silently write real generations and points into whatever the default collection already contains. This happened twice for real during this session's own live-verification work against this exact codebase — once writing 732,078 real points into the actual production `ragctl` collection in ~35 minutes before being caught by a manual point-count check, not by anything ragctl itself flagged.

## Non-goals
- No change to ambient sync itself being the default (SCOPE-002's own deliberate choice, unaffected) — the fix is about collision *detection/prevention*, not about making sync opt-in.
- No multi-tenancy or access-control system — this is about accidental collision between independently-run instances sharing infrastructure, not adversarial isolation.
- No change to `DisableAmbient`'s own default — that flag already exists and already solves this for anyone who knows to set it; the gap is that nothing prompts a new/isolated instance to consider it.

## Design direction (not finalized)
At minimum, a documentation fix: `ragctl init` and the config docs should explicitly call out that `vector.collection`'s default is not automatically scoped per-machine/per-installation, and that pointing two instances at the same Qdrant server without changing it *will* commingle data on the very first `scan`.

Stronger option worth weighing: a startup/first-sync safety check — before the daemon's first-ever write to a collection, if that collection already contains points whose payload metadata doesn't overlap with anything this instance's own `control.db` has ever registered (e.g. dependency names/generations this instance has no record of), warn loudly (or, behind a config flag, refuse) rather than silently proceeding. This mirrors the existing `syncVersion` re-check discipline (epic 55/POINT-004) of verifying state before writing, applied one level up (collection-level, not generation-level).

Cheapest real mitigation, worth doing regardless of the above: change `Default`'s collection name from the plain `"ragctl"` literal to something that at least varies by machine or install (e.g. incorporating hostname or a generated instance ID), so the *default* itself is far less likely to collide — a deliberate choice, not a hardcoded shared name every fresh `ragctl init` produces identically.

## Inputs / Outputs
- Input: a fresh `ragctl init` on a machine/environment that can already reach an existing Qdrant collection with real data in it (shared infra, a second install, a test harness).
- Output: either a clear warning before any write happens, or a meaningfully collision-resistant default collection name — ideally both.

## Failure behavior
- A collision must never be silent. Today it is: no error, no warning, just successful-looking sync output while writing into someone else's data.

## Tests
- A test simulating two `ragctl` configs with the default collection name against one Qdrant instance: confirm whichever mitigation is chosen actually surfaces before/during the second instance's first write, not just in documentation.

## Acceptance criteria
- [x] Config/init documentation explicitly warns about the unscoped-default-collection + ambient-sync-on-first-scan interaction.
- [x] A real design decision made and implemented for the stronger option — both were done, not just one: a collision-resistant default collection name, and a warn-on-foreign-data startup check.
- [x] Live-verified: a second isolated instance pointed at a collection with pre-existing, unrelated data produces a visible warning before writing, not after.

## Implementation notes (2026-09-28)
Did both options the ticket's own design direction weighed, not just the cheaper one:

1. **Collision-resistant default** (`defaultCollectionName()`, `internal/config/config.go`): `Default()`'s `Vector.Collection` is now `"ragctl-" + 8 random hex chars` (via `crypto/rand`), not the old plain `"ragctl"` literal every install shared identically. Deliberately random, not hostname-based — the real incidents this session were two installs on the *same* machine (isolated test `$HOME` directories), which a hostname-derived name would never have distinguished. Only affects fresh installs: `Load` never re-derives a default for a field already on disk, so existing `config.yaml` files are unaffected. Live-verified: two real `ragctl init` runs produced `ragctl-6623dbaa` and `ragctl-5d0b7d70`.
2. **Warn-on-foreign-data startup check** (`checkForeignCollectionData`, new `internal/cli/foreign_collection.go`): launched once at daemon startup (`runDaemonRun`, alongside the existing `checkEmbeddingReadiness`/`checkVectorReadiness` goroutines). If this instance's `control.db` has never registered an active generation (fresh) *and* its configured collection already has real points, logs one loud `WARNING` naming the real point count — deliberately a warning, not a refusal, matching this project's MCP-005 precedent ("warn, don't silently block"), since a deliberately shared/reused collection is a legitimate choice this check can't distinguish from an accidental collision. Live-verified end to end in a fresh isolated container: seeded a collection with fake pre-existing data, pointed a fresh instance at the same collection name, confirmed the warning appeared in the daemon's own log file *before* any scan or sync ever ran.

Docs updated: `docs/internal/config.md`'s Notes section explains both mechanisms and their scope/limits explicitly.

Tests: `TestDefaultCollectionNameIsUniquePerInstall` (`internal/config/config_test.go`); `TestCheckForeignCollectionDataWarnsOnFreshInstanceWithExistingData`/`...SilentWhenAlreadyRegistered`/`...SilentWhenCollectionEmpty` (`internal/cli/foreign_collection_test.go`). Full native test suite, `go vet`, and `go build ./...` all clean.

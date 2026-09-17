# STORE-004: Storage integration boundary

**Epic:** Core Domain and Storage
**Status:** done
**Depends on:** STORE-001, STORE-003
**Estimated size:** small

## Goal
Prove bbolt (control plane) and Badger (data plane) work correctly together across a process restart. This is the project's first persistence checkpoint and a prerequisite for `ragctl init`.

## Non-goals
- Do not build a unifying "service" interface/facade over both stores unless a second caller genuinely needs one. The plan explicitly says: "Create service-level store contracts only if needed. Prefer concrete types internally."

## Simplicity constraints
- This ticket is primarily a test, not new production code. Only add glue code (e.g. a small `internal/app` struct holding both `*bboltstore.Store` and `*badgerstore.Store`) if CLI-002 (`ragctl init`) needs it — otherwise keep it to a test file.

## Design
Add an integration test (e.g. `internal/app/storage_integration_test.go` or `internal/control/bbolt/integration_test.go` with a Badger import) that:
1. Opens both stores at temp paths.
2. Writes a `Project` and a `ProjectDependency`/reference to bbolt.
3. Writes generation content (a `KnowledgeObject`, a `Chunk`, a manifest) to Badger for a generation ID.
4. Marks that generation `ACTIVE` via `Store.SetActiveGeneration` in bbolt.
5. Closes both DBs.
6. Reopens both at the same paths.
7. Verifies the active generation pointer and the Badger content are both present and correct.

If a thin `app.Storage` struct emerges naturally (bundling `Open`/`Close` for both), keep it to just that — open/close lifecycle, nothing else.

## Inputs / Outputs
- Input: temp directory paths for both stores.
- Output: pass/fail test result; optionally a small `Storage` struct usable by CLI-002.

## Failure behavior
N/A — this is a test ticket; failures surface as test failures.

## Tests
- The 6-step scenario in Design, run as a single `TestStorageRestartPersistence` (or similarly named) test.

## Acceptance criteria
- [x] Test writes to both bbolt and Badger, closes both, reopens both, and verifies data integrity across the restart.
- [x] Active generation pointer survives restart.
- [x] Badger-side generation content survives restart.

## Post-implementation note
Implemented as `TestStorageRestartPersistence` in `internal/control/bbolt/integration_test.go`, package `bbolt_test` (external test package, not `bbolt`) — deliberately, so the test can import both `internal/control/bbolt` and `internal/data/badger` through their exported APIs only, without creating any new production import between the two packages. No `app.Storage` facade was added, per this ticket's own explicit warning; nothing today needs one beyond what the CLI's own `openControlStore`/`openDataStore` helpers already do.

The test follows the ticket's 7-step scenario: opens both stores, writes a `Project`, a `Generation` (`PutGeneration`), a `KnowledgeObject` and one `Chunk` and a manifest to Badger, promotes the generation active (`PromoteGeneration`), closes both, reopens both, then — the specific cross-store step this ticket exists to prove, not just "each store independently persisted its own data" — resolves the active generation's ID from bbolt (`GetActiveGeneration`) and uses *that* ID to read the chunk/object/manifest back out of Badger, confirming the two databases still agree with each other after independent restarts. Passes under `-race`.


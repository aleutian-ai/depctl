# PROJ-002: Stable project IDs

**Epic:** Project Discovery
**Status:** done
**Depends on:** PROJ-001
**Estimated size:** small

## Goal
Define how a `Project.ID` is derived so it survives process restarts and rescans, and document move/rename behavior explicitly.

## Non-goals
- No automatic move/rename detection in v0.1 — a moved project is treated as new unless the user explicitly re-links it (no such command exists yet either; this is a documented limitation, not a missing feature to build).

## Simplicity constraints
- A pure hash function of the canonical path is simpler and sufficient for v0.1 — do not build a persisted "generated ID keyed by root" indirection table unless the hash approach proves inadequate (it won't, for v0.1's needs).

## Design
Package: `internal/project` (alongside PROJ-001).

```go
func ProjectID(canonicalRoot string) string // "proj_" + base32(BLAKE3(canonicalRoot))
```
`canonicalRoot` = `filepath.Abs` + `filepath.Clean` + (on case-insensitive filesystems, document that this is *not* normalized — out of scope) of the project root directory.

Document explicitly (in a package doc comment, not just this ticket) the v0.1 policy:
> Moving or renaming a project directory produces a new project ID on the next scan. The old project's dependency references and reasons are not automatically transferred. A future `ragctl project move` command may address this; it does not exist in v0.1.

## Inputs / Outputs
- Input: canonical absolute path string.
- Output: deterministic project ID string.

## Failure behavior
N/A — pure deterministic function, no error path (assuming a valid path string is passed; empty string is a programmer error, not a runtime error to handle gracefully).

## Tests
- Same path in, same ID out, across multiple calls and process restarts (encode expected ID as a golden value in the test to catch accidental hash-input changes).
- Different paths produce different IDs (no accidental collisions in a reasonable sample).
- Path with trailing slash / `.` components normalizes to the same ID as the clean form.

## Acceptance criteria
- [x] `ProjectID` is deterministic across restarts (golden-value test).
- [x] Rename/move behavior is documented in code comments and matches the "new project" v0.1 policy.

# PROJ-003: `ragctl scan`

**Epic:** Project Discovery
**Status:** planned
**Depends on:** PROJ-001, STORE-001, CLI-003
**Estimated size:** small

## Goal
Wire the project scanner and stable IDs into the `ragctl scan <path>` CLI command, persisting discovered projects to the bbolt store.

## Non-goals
- No dependency resolution triggered automatically from `scan` in this ticket — `scan` only registers projects. (Whether `sync`/`plan` chains after scan is a later planner concern.)

## Simplicity constraints
- `scan`'s job is: walk → compute IDs → upsert into bbolt → print a summary. No additional orchestration.

## Design
Command: `ragctl scan [path]` (default `path` = current directory), implemented in `internal/cli`, calling `internal/project.Scan` and `bboltstore.Store.PutProject`/`ListProjects`.

For each `DetectedProject`:
1. Compute `ProjectID` (PROJ-002).
2. If a project with that ID already exists in bbolt → classify as `existing`.
3. Else → `PutProject`, classify as `new`.
4. Ecosystems with no matching resolver implemented yet → classify as `unsupported` (this only matters once ecosystems beyond Go start landing; for the very first cut, only `go` is "supported").

Output format (human-readable table or summary), grouped into:
```
discovered
new
existing
unsupported
```
matching the plan's exact bucket names.

## Inputs / Outputs
- Input: a root path argument.
- Output: bbolt `projects` bucket updated; stdout summary with the four categories.

## Failure behavior
- Scan errors (e.g. bad root path) abort with a clear message and non-zero exit.
- Per-project persistence errors are collected and reported per-project rather than aborting the whole scan.

## Tests
- Idempotency: running `ragctl scan <path>` twice on an unchanged tree produces zero `new` projects the second time and no duplicate bbolt entries.
- Fixture tree from PROJ-001 scanned end-to-end, verify bbolt contains 4 projects after scan.

## Acceptance criteria
- [ ] Repeated scan is idempotent (no duplicate projects).
- [ ] Output clearly separates discovered/new/existing/unsupported.
- [ ] Scanned projects are persisted and visible via `Store.ListProjects` after the command exits.

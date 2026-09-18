# STRESS-004: Multi-ecosystem monorepo scan

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Scan a real (or realistically constructed) monorepo containing both a Go module and a Node package (`supportedEcosystems` in `internal/cli/scan.go` already enables Go, Python, and Node) and confirm `scan` correctly dispatches to and completes both resolvers in one pass — registering two distinct projects (or one project with two resolutions, depending on how the directory walk actually groups them) with correct, independent dependency lists.

## Non-goals
- No new resolver work — this exercises the three resolvers already shipped (epics 06/20/21), it doesn't add ecosystem coverage.
- No sync/gc/serve in this ticket — scan only.

## Simplicity constraints
- Construct a small-but-real monorepo rather than searching for a pre-existing public one: real resolution (real `go list`/`npm`), just not a large-scale fixture (that's STRESS-001's job for Go specifically).

## Design
1. Construct the monorepo:
   - `go-side/go.mod`: `module example.com/monorepo-go` requiring **`github.com/spf13/cobra`** (a real, moderately-sized dependency tree of its own — `spf13/pflag`, `inconshreveable/mousetrap`, etc. resolve transitively), plus a one-line `main.go` that imports it so `go list` has something real to walk.
   - `node-side/package.json`: a real `package.json` requiring **`express`** and **`lodash`** (`npm install express lodash` to generate a real `package-lock.json`/`node_modules` resolution, matching how the Node resolver actually reads a project).
2. `ragctl scan <monorepo-root>`.
2. `ragctl scan <root>` — walks the directory tree, should discover both the Go and Node projects.
3. `ragctl project list` — confirm both projects are registered.
4. `ragctl deps <go-project-id>` and `ragctl deps <node-project-id>` — confirm each lists only its own ecosystem's real dependencies, no cross-contamination (a Go dependency never appears under the Node project's resolution or vice versa).

## Inputs / Outputs
- Input: one monorepo root containing both a Go and a Node project.
- Output: pass/fail on correct discovery of both, and correct, non-cross-contaminated per-project dependency lists.

## Failure behavior
- A missed project (only one of the two discovered), a merged/cross-contaminated resolution, or a resolver dispatch error is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] Both the Go and Node projects in the monorepo are discovered and registered by one `scan` invocation.
- [ ] Each project's resolved dependency list contains only its own ecosystem's real dependencies.

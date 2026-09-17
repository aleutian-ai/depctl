# BOOT-001: Create repository skeleton

**Epic:** Bootstrap
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Create the initial repository directory layout and required top-level files so the project is buildable and lint/test tooling has somewhere to run.

## Non-goals
- No actual application code — this is scaffolding only.
- No CI workflow content beyond a placeholder (see BOOT-003).
- No toolchain version pinning (see BOOT-002).

## Simplicity constraints
- Do not pre-create every package directory implied by later milestones (e.g. `internal/backend/weaviate`). Only create the directories needed to make `go build ./...` and `go test ./...` succeed against an empty module.
- Do not add a Makefile, task runner, or build system abstraction yet — plain `go` commands are sufficient at this stage.

## Design
Create top-level directories:
```
cmd/ragctl
internal/
pkg/
schemas/
docs/
examples/
testdata/
```
Add a minimal `cmd/ragctl/main.go` with a `func main()` that prints a placeholder message, so `go build ./...` has something to compile.

Required files at repo root:
```
README.md
LICENSE            (Apache-2.0)
SECURITY.md
CONTRIBUTING.md
CODE_OF_CONDUCT.md
go.mod
.golangci.yml       (minimal config, can be near-empty)
.github/workflows/test.yml   (placeholder, see BOOT-003)
```

## Inputs / Outputs
- Input: none (first commit).
- Output: a buildable, empty Go module.

## Failure behavior
N/A — this is static scaffolding, no runtime behavior.

## Tests
- `go build ./...` succeeds.
- `go vet ./...` succeeds.
- `go test ./...` succeeds (even with zero tests, exit code 0).

## Acceptance criteria
- [x] Directory layout matches the design section.
- [x] All required root files exist and are non-empty.
- [x] `go build ./...`, `go vet ./...`, and `go test ./...` all succeed on a clean checkout.

## Post-implementation note
`schemas/` was never created as a top-level directory — deliberately, not an oversight. REG-001 (`docs/tickets/completed/07-knowledge-registry`) found that `go:embed` can't reach outside a package's own directory tree, so the KnowledgePackage JSON Schema lives at `internal/registry/schema/knowledge-package.schema.json` instead of a repo-root `schemas/` dir that would have needed duplicating into the package anyway. Every other directory in the design section exists as specified. `SECURITY.md`/`CONTRIBUTING.md`/`CODE_OF_CONDUCT.md`/`.golangci.yml`/`.github/workflows/test.yml` were the last gap, closed alongside BOOT-003.

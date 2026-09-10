# BOOT-002: Lock Go toolchain version

**Epic:** Bootstrap
**Status:** planned
**Depends on:** BOOT-001
**Estimated size:** small

## Goal
Choose and record a single supported Go toolchain version so local development and CI stay in sync.

## Non-goals
- No multi-version build matrix. One version, full stop, for v0.1.

## Simplicity constraints
- Do not attempt to support a range of Go versions. Pick the current stable release at project start and pin it everywhere.
- Do not use language features newer than the chosen minimum version anywhere in the codebase.

## Design
- Set the `go` directive in `go.mod` to the chosen version.
- Record the same version in `README.md` (a "Requirements" section) and in `.github/workflows/test.yml` (`actions/setup-go` `go-version` input, see BOOT-003).
- Add a `ragctl doctor`-visible diagnostic later (out of scope here) that reports `go env GOVERSION`; for this ticket, just ensure the version is consistent across the three locations above.

## Inputs / Outputs
- Input: chosen Go version (a decision, not code).
- Output: `go.mod`, `README.md`, and CI config all reference the identical version string.

## Failure behavior
N/A — configuration only.

## Tests
- Script/manual check: grep the version string out of `go.mod`, `README.md`, and `.github/workflows/test.yml` and assert they match.

## Acceptance criteria
- [ ] `go.mod` `go` directive set.
- [ ] README documents the required Go version.
- [ ] CI config uses the same version.
- [ ] Local `go version` output matches what CI installs (documented, not automatically enforced in this ticket).

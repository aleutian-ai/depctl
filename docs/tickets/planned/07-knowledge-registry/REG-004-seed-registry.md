# REG-004: Seed registry

**Epic:** Knowledge Registry
**Status:** planned
**Depends on:** REG-003
**Estimated size:** small

## Goal
Author a handful of real, hand-written manifests in the built-in registry to validate the schema and matching end-to-end — not to achieve package coverage.

## Non-goals
- Does not seed hundreds of packages. This is explicitly a format-validation exercise.
- Does not build tooling for automated manifest generation.

## Simplicity constraints
- Hand-author each manifest; do not build a scraper/generator for this ticket.

## Design
- Location: `internal/registry/builtin/*.yaml` (embedded via `embed.FS`, loaded by REG-002).
- Initial packages (from the implementation plan):
  ```text
  Go:
    google.golang.org/grpc
    google.golang.org/protobuf
    github.com/dgraph-io/badger/v4
    go.etcd.io/bbolt

  Python:
    pydantic
    fastapi
  ```
- Each manifest follows the REG-001 schema exactly, including at minimum a `repository` (git) source and, where sensible, a `godoc` or `website` source with realistic authority scores per the design spec's authority table (100 = exact tagged source, 95 = official API docs/release notes, 90 = official conceptual docs).

## Inputs / Outputs
- Input: none (static YAML authored by hand).
- Output: 6 manifest files passing REG-001 schema validation and REG-002 loading.

## Failure behavior
- N/A — these are static fixtures; CI should run REG-001's schema validation against every file in `internal/registry/builtin/` to prevent regressions.

## Tests
- All 6 seed manifests parse and validate against the JSON Schema.
- REG-003 `Match` resolves each seeded ecosystem+package pair to its manifest.

## Acceptance criteria
- [ ] 6 manifests committed and passing schema validation in CI.
- [ ] `Match("go", "google.golang.org/grpc")` (and the other 5) resolve correctly.

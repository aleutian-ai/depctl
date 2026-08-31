# NODE-004: Yarn resolver

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** planned
**Depends on:** NODE-001
**Estimated size:** medium

## Goal
Parse `yarn.lock` (Yarn Classic v1 and Berry formats) to resolve exact package versions.

## Non-goals
- Yarn PnP (Plug'n'Play) resolution details beyond reading the lockfile's resolved version.
- Zero-installs cache inspection.

## Simplicity constraints
- Ship only after NODE-002/003 are stable; this is explicitly schedulable after npm/pnpm per the implementation plan.
- Yarn Classic's lockfile is a custom (non-YAML, non-JSON) format — write the smallest line-based parser that handles the `name@range:` block + `version "x.y.z"` pattern. Do not pull in a third-party yarn-lock parser dependency unless the hand-rolled parser proves unreliable on real fixtures.
- Yarn Berry lockfiles are valid YAML — reuse the yaml.v3 dependency already in the module.

## Design
Package: `internal/resolver/node` (file `yarn.go`).

Detect format by checking for a `__metadata:` YAML key (Berry) vs classic block syntax. Two small parsing paths behind one function:
```go
func parseYarnLock(data []byte) ([]entry, error)
```
Each entry: package name, resolved version, whether it's a workspace/local reference (`workspace:` protocol in Berry, or `link:`/`file:` ranges).

## Inputs / Outputs
- Input: project root with `yarn.lock`.
- Output: `domain.Resolution` per NODE-002 shape.

## Failure behavior
- Unrecognized format → typed `ResolutionError` naming the file.

## Tests
- Yarn Classic fixture: direct + transitive dependency.
- Yarn Berry fixture: workspace member with `workspace:` reference marked local.

## Acceptance criteria
- [ ] Both Yarn Classic and Berry fixtures resolve to correct exact versions.

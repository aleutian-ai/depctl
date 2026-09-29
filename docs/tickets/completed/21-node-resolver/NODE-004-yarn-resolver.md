# NODE-004: Yarn resolver

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** done — see Post-implementation note
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
- [x] Both Yarn Classic and Berry fixtures resolve to correct exact versions.

## Post-implementation note

Built as designed — `internal/resolver/node/yarn.go`, a hand-rolled line-based parser for Classic (no third-party dependency, matching this ticket's own simplicity constraint), `yaml.v3` (already a module dependency, via pnpm.go's precedent) for Berry.

**One real design decision beyond the original sketch**: yarn.lock (both formats) carries no reliable "these are the project's own direct dependencies" section the way `package-lock.json`'s root entry or `pnpm-lock.yaml`'s `importers` do — Berry's own workspace self-entries come closest, but Classic has nothing analogous at all. Rather than building two different direct-detection mechanisms (one per format), `resolveYarnLock` reads `package.json`'s own `dependencies`/`devDependencies` uniformly for both — simpler, and Detect already guarantees `package.json` exists before `Resolve` is ever called.

**Berry's `linkType: soft` entries** (a workspace's own self-entry, or a sibling workspace reached via the `workspace:` protocol) are skipped entirely rather than kept with a "local" marker the way `npm.go` keeps symlinked packages — unlike npm's case, there's no real resolvable version to report at all (Berry gives these a placeholder like `0.0.0-use.local`).

**Verified against real, live-generated lockfiles, not just hand-written fixtures** — matching this session's established discipline: installed Yarn 1.22.22 and 4.5.0 via `corepack`, ran real `yarn install` against a two-dependency (`is-odd`/`is-number`) test project for each, and both the resulting real `yarn.lock` files (inlined as permanent test fixtures, `TestParseYarnClassicRealGeneratedLockfile`/`TestParseYarnBerryRealGeneratedLockfile`) and a full `Resolver.Resolve()` run directly against the real generated project directories parsed correctly — exact versions, correct direct/transitive flags, the Berry workspace self-entry correctly excluded.

Full suite green, `-race` clean.

# REG-003: Registry matcher

**Epic:** Knowledge Registry
**Status:** done
**Depends on:** REG-002
**Estimated size:** small

## Goal
Given an ecosystem + package identity (e.g. `go` / `google.golang.org/grpc`), return the matching manifest's knowledge sources and version-mapping strategy.

## Non-goals
- Does not fetch or acquire any source content (GIT-001+).
- Does not handle fuzzy/partial matching beyond exact ecosystem+package membership in a manifest's `match` block.

## Simplicity constraints
- Matching is an exact-membership lookup (`ecosystem ∈ match.ecosystems AND package ∈ match.packages`) — no globbing or regex matching in v0.1 even though the design doc mentions scoped Node packages (`@types/*`) as a future case; build a plain index for v0.1 and note scoped-pattern matching as a follow-up if REG-004 fixtures need it.

## Design
- Package: `internal/registry`
- Build an index at `Registry` construction time: `map[ecosystemPackageKey]Manifest` for O(1) lookup.
- Function:
  ```go
  func (r *Registry) Match(eco domain.Ecosystem, pkg string) (Manifest, bool)
  ```
- Returns the manifest's `Sources` and `Version.Strategy`/`Version.Repository` for downstream acquisition planning.

## Inputs / Outputs
- Input: ecosystem, package name (as resolved by the ecosystem resolver, e.g. GO-003's normalized name).
- Output: `(Manifest, found bool)`.

## Failure behavior
- No match found is not an error — it's `(Manifest{}, false)`; caller (planner) records the package as having no knowledge source and reports it in sync output, per the design spec's registry discovery order (item 5: "unresolved knowledge source").

## Tests
- Exact Go module match (`google.golang.org/grpc`) returns the grpc-go manifest.
- Package with no manifest returns `found=false`, no panic/error.
- (Deferred/optional) Scoped Node package fixture and Maven group:artifact fixture, to be added once NODE-*/JAVA-* resolvers exist — stub test placeholders acceptable now.

## Acceptance criteria
- [x] Exact Go module match works end-to-end against a loaded `Registry`.
- [x] Unmatched packages return cleanly without error.

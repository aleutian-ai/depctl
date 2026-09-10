# GO-003: Normalize Go dependency identity

**Epic:** Go Resolver
**Status:** done
**Depends on:** GO-002
**Estimated size:** small

## Goal
Convert raw `go list` module output into the domain's canonical `DependencyVersion` list (from CORE-001), applying replace/local/main-module rules.

## Non-goals
- Does not persist anything (GO-004).
- Does not compute a resolution fingerprint (GO-004).

## Simplicity constraints
- One pure function, no I/O. Keep it a straightforward slice transform — do not introduce a generic "normalizer pipeline" abstraction for a single ecosystem.

## Design
- Package: `internal/resolver/golang`
- Function:
  ```go
  func normalize(modules []goModule) ([]domain.DependencyVersion, error)
  ```
- Rules:
  - `ecosystem = domain.EcosystemGo`
  - `name = module.Path`
  - `version = module.Version` (or the resolved replace target's version if `Replace != nil` and `Replace.Version != ""`)
  - If `Replace != nil` and `Replace.Version == ""` (i.e. a filesystem path replace), mark the dependency `local` (e.g. a `Local bool` field or a metadata tag) and exclude it from external knowledge sync.
  - If `Main == true`, exclude the module entirely from the output (it's the project itself, not a dependency).
  - When replaced, retain both the logical dependency (original `Path`) and the resolved source (`Replace.Path`, `Replace.Version`) — store both in the `DependencyVersion` (e.g. via `ResolvedBy` / metadata field) so provenance isn't lost.

## Inputs / Outputs
- Input: `[]goModule` from GO-002.
- Output: `[]domain.DependencyVersion`, each tagged direct/indirect (`Indirect` field), local or not.

## Failure behavior
- A module with an empty `Path` is a data integrity error — return a typed error, don't skip silently.

## Tests
- Main module excluded from output.
- Local path replace marked `local`, excluded from sync-eligible list downstream (flag present, filtering happens in GO-004/PLAN layer).
- Version replace produces the replaced version, with original path preserved.
- Direct vs indirect flag passed through correctly.

## Acceptance criteria
- [x] Main module never appears in normalized output.
- [x] Local replacements are clearly flagged as `local`.
- [x] Replaced modules retain both logical dependency and resolved source info.

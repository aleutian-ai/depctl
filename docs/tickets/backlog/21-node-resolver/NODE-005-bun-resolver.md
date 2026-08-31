# NODE-005: Bun resolver

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** planned
**Depends on:** NODE-001
**Estimated size:** small

## Goal
Parse `bun.lock` (Bun's text lockfile format, JSONC-like) to resolve exact package versions.

## Non-goals
- Bun's binary `bun.lockb` format — only the newer text `bun.lock` is required for v1.

## Simplicity constraints
- Can be scheduled after Yarn/Bun per the implementation plan; do not block other Node resolvers on this.
- `bun.lock` is JSON with comments (JSONC) — strip comments with a minimal pass, or shell out to `bun bun.lock` if Bun is available, whichever is less code. Prefer a pure-Go text parser to avoid a runtime dependency on the `bun` binary being installed.

## Design
Package: `internal/resolver/node` (file `bun.go`).

```go
func parseBunLock(data []byte) ([]entry, error)
```
Reuses the same `entry`/`domain.Resolution` shape as NODE-002/003/004.

## Inputs / Outputs
- Input: project root with `bun.lock`.
- Output: `domain.Resolution`.

## Failure behavior
- Unparseable file → typed `ResolutionError`.

## Tests
- Small fixture with direct + transitive dependency.

## Acceptance criteria
- [ ] Fixture resolves to correct exact versions.
- [ ] Scoped package names preserved.

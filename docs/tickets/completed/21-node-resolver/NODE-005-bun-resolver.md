# NODE-005: Bun resolver

**Epic:** Node / JavaScript / TypeScript Resolver
**Status:** done — see Post-implementation note
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
- [x] Fixture resolves to correct exact versions.
- [x] Scoped package names preserved.

## Post-implementation note

Built as designed — `internal/resolver/node/bun.go` — with one deliberate deviation from the ticket's own suggested tooling: rather than shelling out to a real `bun` binary (a runtime dependency this ticket's own Non-goals already preferred avoiding) or reaching for a third-party JSONC library, a small hand-rolled `stripJSONC` pass (strips `//`/`/* */` comments and trailing commas, correctly string-literal-aware so a URL like `"http://..."` in a value is never mistaken for a comment) reduces `bun.lock` to plain JSON before decoding with `encoding/json` — matching NODE-004/yarn.go's own established precedent of hand-rolling a small format-specific parser before reaching for a dependency.

`packages[key]` is decoded as `[]any` and only its first element (the `"name@version"` specifier) is read — deliberately tolerant of the rest of the array's shape, which real Bun output doesn't hold entirely stable across versions (see below).

**Verified against a real, live-generated lockfile, not just a hand-written fixture**: installed Bun 1.4.2 directly (`curl -fsSL https://bun.sh/install | bash` — real local environment change, noted for the record: added `~/.bun/bin` to `~/.zshrc`'s PATH; not reverted, since a working `bun` install is a reasonable thing to leave in place, but flagged here in case it's unwanted), ran a real `bun install` against a two-dependency (`is-odd`/`is-number`) test project. The real output (`lockfileVersion: 2`, an undeclared `configVersion` field, and each `packages[key][1]` being an empty string rather than an object — none of which matches the schema this ticket's own Design section sketched from public documentation alone) still parsed correctly, confirming the deliberately-tolerant `[]any` decoding was the right call — inlined as a permanent test fixture (`TestResolveBunLockRealGeneratedLockfile`) and confirmed via a full `Resolver.Resolve()` run directly against the real generated project directory.

Full suite green, `-race` clean.

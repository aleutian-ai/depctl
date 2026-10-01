# RUST-002: `cargo metadata` resolver

**Epic:** Rust Resolver
**Status:** declined (2026-09-30)
**Depends on:** RUST-001, RES-002
**Estimated size:** medium

## Goal
Execute `cargo metadata --format-version 1` via the shared command runner (`internal/executil`, from RES-002) and parse exact resolved package versions, preserving registry vs Git vs path dependency source, commit/revision for Git deps, and workspace membership.

## Non-goals
- Reimplementing Cargo's dependency solver — always defer to `cargo metadata`.
- Handling every possible Cargo.lock edge case not surfaced by `cargo metadata`'s JSON output.

## Simplicity constraints
- Do not parse `Cargo.lock` directly; `cargo metadata` is the single source of truth (per implementation plan §3.5: prefer official package-manager resolution).
- Only decode the JSON fields actually needed (`packages[].name/version/source/id`, `resolve.nodes`, `workspace_members`) — do not model the entire `cargo metadata` schema.

## Design
Package: `internal/resolver/rust`

```go
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) {
    out, err := executil.Run(ctx, root, "cargo", "metadata", "--format-version", "1")
    ...
}
```
Parse `source` field to classify:
- `nil`/absent source + path under workspace root → path dependency → mark `local`.
- `"registry+https://github.com/rust-lang/crates.io-index"` → normal crates.io dependency.
- `"git+https://github.com/org/foo?rev=abc123"` → Git dependency; identity must include the commit/rev, not collapse to "latest foo" (per design spec §15 example).

Normalized identity:
```text
ecosystem = rust
name = crate name
version = resolved version (or commit for git deps)
```

## Inputs / Outputs
- Input: project root containing `Cargo.toml`/`Cargo.lock`.
- Output: `domain.Resolution{Ecosystem: EcosystemRust, Dependencies: [...], Fingerprint}`.

## Failure behavior
- `cargo` binary missing → typed `ResolutionError` ("cargo not found on PATH"), surfaced later by `ragctl doctor`.
- Non-zero exit / malformed JSON → typed `ResolutionError` with captured stderr.
- Context cancellation → propagate cancellation error from `executil`.

## Tests
- Registry dependency fixture.
- Path dependency marked local.
- Git dependency with `rev` preserved distinctly (not treated as "latest").
- Workspace fixture: multiple workspace members resolve independently.
- Cancelled context mid-execution.

## Acceptance criteria
- [ ] Same `Cargo.toml`/`Cargo.lock` yields a stable resolution fingerprint across repeated runs.
- [ ] Git dependencies retain commit/rev identity.
- [ ] Path dependencies marked local and excluded from external knowledge sync.


## Declined (2026-09-30)

Not being built — a deliberate priority call, not a technical blocker. Reviewed against the shipped `Resolver` interface (`internal/resolver`) during a backlog-triage discussion: it is already fully ecosystem-agnostic (`Name() string`, `Detect(ctx, root) (bool, error)`, `Resolve(ctx, root) (domain.Resolution, error)`), `domain.Ecosystem` already carries unused `EcosystemRust`/`EcosystemJava` constants, and `domain.Resolution`/`DependencyVersion`/`Dependency` have zero ecosystem-specific fields — exactly the same shape Go/Python/Node's real resolvers already prove out (shell out to the ecosystem's own tool, parse its structured JSON output, map to `domain.Resolution`). This ticket's own design already matches that pattern. Nothing here needs re-scoping or new interface work if picked up later — it was never the hard part.

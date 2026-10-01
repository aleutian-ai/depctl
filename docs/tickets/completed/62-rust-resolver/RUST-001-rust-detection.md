# RUST-001: Rust project detection

**Epic:** Rust Resolver
**Status:** declined (2026-09-30)
**Depends on:** RES-001
**Estimated size:** small

## Goal
Detect Rust project roots by locating `Cargo.toml` (and note presence of `Cargo.lock`).

## Non-goals
- Dependency resolution (RUST-002).
- Workspace member enumeration beyond what `cargo metadata` will provide later.

## Simplicity constraints
- No TOML-parsing logic needed here — presence check only (`os.Stat`). Defer any TOML parsing to RUST-002 where `cargo metadata` output is used instead of hand-parsing `Cargo.toml`.

## Design
Package: `internal/resolver/rust`

```go
type Resolver struct{}

func (r *Resolver) Name() string { return "rust" }
func (r *Resolver) Detect(ctx context.Context, root string) (bool, error)
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) // implemented in RUST-002
```
`Detect` returns true if `Cargo.toml` exists at `root`.

## Inputs / Outputs
- Input: candidate directory.
- Output: bool detected.

## Failure behavior
- Missing file → `false, nil`.
- Unreadable directory → error.

## Tests
- `testdata/projects/rust-cargo/` fixture detected.
- Non-Rust directory not detected.

## Acceptance criteria
- [ ] Detection works on real small Cargo project fixture.


## Declined (2026-09-30)

Not being built — a deliberate priority call, not a technical blocker. Reviewed against the shipped `Resolver` interface (`internal/resolver`) during a backlog-triage discussion: it is already fully ecosystem-agnostic (`Name() string`, `Detect(ctx, root) (bool, error)`, `Resolve(ctx, root) (domain.Resolution, error)`), `domain.Ecosystem` already carries unused `EcosystemRust`/`EcosystemJava` constants, and `domain.Resolution`/`DependencyVersion`/`Dependency` have zero ecosystem-specific fields — exactly the same shape Go/Python/Node's real resolvers already prove out (shell out to the ecosystem's own tool, parse its structured JSON output, map to `domain.Resolution`). This ticket's own design already matches that pattern. Nothing here needs re-scoping or new interface work if picked up later — it was never the hard part.

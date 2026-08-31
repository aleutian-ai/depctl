# RUST-001: Rust project detection

**Epic:** Rust Resolver
**Status:** planned
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

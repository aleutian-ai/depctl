# Epic: Rust Resolver

**Declined (2026-09-30)** — not a technical blocker, a deliberate priority call. Backlog triage confirmed the shipped `Resolver` interface (`internal/resolver`) is already fully ecosystem-agnostic and `domain.Ecosystem` already carries an unused `EcosystemRust` constant — adding Rust support later is "write one more implementation of an interface that already exists," not an architecture project. See each ticket's own "Declined" note for the full reasoning. Not prioritized right now; trivial to pick back up whenever a real Rust project needs ragctl.

Detect Cargo-based Rust projects and resolve exact dependency versions using `cargo metadata`, never a hand-rolled Cargo dependency solver.

- [RUST-001](RUST-001-rust-detection.md) — detect `Cargo.toml`/`Cargo.lock`.
- [RUST-002](RUST-002-cargo-metadata.md) — run and parse `cargo metadata --format-version 1`, preserving registry/Git/path source and workspace membership.

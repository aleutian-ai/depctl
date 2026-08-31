# Epic: Rust Resolver

Detect Cargo-based Rust projects and resolve exact dependency versions using `cargo metadata`, never a hand-rolled Cargo dependency solver.

- [RUST-001](RUST-001-rust-detection.md) — detect `Cargo.toml`/`Cargo.lock`.
- [RUST-002](RUST-002-cargo-metadata.md) — run and parse `cargo metadata --format-version 1`, preserving registry/Git/path source and workspace membership.

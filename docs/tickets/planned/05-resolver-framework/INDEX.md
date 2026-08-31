# Epic: Resolver Framework

The shared contract and plumbing every ecosystem-specific dependency resolver (Go, Python, Node, Rust, Java) is built on: a common `Resolver` interface plus a safe, reusable subprocess-execution helper. No ecosystem-specific logic lives here.

## Tickets
- [RES-001 — Resolver interface](RES-001-resolver-interface.md): `Resolver` interface (`Name`/`Detect`/`Resolve`), `Resolution` type, resolver registry.
- [RES-002 — Command execution helper](RES-002-command-execution-helper.md): `internal/executil.Run` — context-aware, no-shell subprocess execution used by every resolver.

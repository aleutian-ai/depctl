# Epic: Go Resolver

The first complete ecosystem resolver and the reference implementation other ecosystems (Python, Node, Rust, Java) will follow. Detects Go module roots, resolves the exact module graph via the `go` toolchain (never a hand-rolled solver), normalizes replace/local/indirect semantics into the domain model, and persists resolutions with a stable fingerprint for idempotency.

## Tickets
- [GO-001](GO-001-detect-go-project.md) — Detect a Go module root via `go.mod` presence.
- [GO-002](GO-002-execute-go-list.md) — Run `go list -m -json all` and parse the module graph.
- [GO-003](GO-003-normalize-go-dependency-identity.md) — Normalize raw modules into canonical `DependencyVersion`s (replace/local/main handling).
- [GO-004](GO-004-persist-go-resolution.md) — Fingerprint and persist the resolution to bbolt.

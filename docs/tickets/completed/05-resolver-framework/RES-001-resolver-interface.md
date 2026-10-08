# RES-001: Resolver interface

**Epic:** Resolver Framework
**Status:** done
**Depends on:** CORE-001
**Estimated size:** small

## Goal
Define the `Resolver` interface that every ecosystem-specific resolver (Go, Python, Node, Rust, Java) implements, plus a `Resolution` result type and a registry for deterministic resolver ordering.

## Non-goals
- No concrete resolver implementations here (Go resolver is GO-001+, etc.).
- No dependency-graph diffing/merging logic — that's the planner's job later.

## Simplicity constraints
- The interface is exactly three methods, matching the design spec verbatim — do not add extra methods (e.g. `Priority()`, `Configure()`) speculatively; a resolver registry ordering fixed list is enough for v0.1.

## Design
Package: `internal/resolver`

```go
type Resolver interface {
    Name() string
    Detect(ctx context.Context, root string) (bool, error)
    Resolve(ctx context.Context, root string) (domain.Resolution, error)
}
```

`Resolution` is defined once, in `internal/domain` (see CORE-001), not here — every ecosystem resolver, the bbolt store (STORE-001), and the planner (PLAN-001) all need it, so it lives in the shared domain package rather than being redefined per-consumer.

Registry:
```go
type Registry struct { resolvers []Resolver }
func NewRegistry(resolvers ...Resolver) *Registry
func (r *Registry) DetectAll(ctx context.Context, root string) ([]Resolver, error) // resolvers whose Detect() returned true, in fixed registration order
```
Registration order is the deterministic priority (no dynamic priority scoring system needed for v0.1 — order of registration in `main.go`/`cmd/depctl` is the priority).

## Inputs / Outputs
- Input: a project root path.
- Output: `Resolution` per matching resolver (a project can match more than one resolver, e.g. polyglot repo — caller runs each independently).

## Failure behavior
`Resolve` returns a typed `ResolutionError` wrapping the underlying cause (missing tool, malformed lockfile, timeout) so callers can distinguish transient vs permanent failures (see RES-002 for the command execution error classification this builds on).

## Tests
- A fake in-memory `Resolver` implementation used to test `Registry.DetectAll` ordering and multi-match behavior.
- `Resolution.Fingerprint` is stable for identical inputs (test via the fake resolver).

## Acceptance criteria
- [x] Interface matches the three-method shape above.
- [x] Fake resolver unit test passes.
- [x] Registry supports deterministic priority (registration order).

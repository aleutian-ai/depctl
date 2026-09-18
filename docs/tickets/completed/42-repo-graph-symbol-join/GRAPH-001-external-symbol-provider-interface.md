# GRAPH-001: External symbol provider interface

**Epic:** Repo-Graph Symbol Join
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Define a thin, narrow interface that lets ragctl ask "what dependency symbol does this call site refer to?" without ragctl building or owning any graph/LSP/symbol-indexing infrastructure itself. This ticket ships only the interface and the struct it returns — no backing implementation.

## Non-goals
- **This is not a control plane.** `SymbolProvider` does not manage indexing lifecycle, does not own a symbol database, does not schedule background graph builds, and does not know anything about a project beyond answering one resolution call. If a future need for orchestration/scheduling around symbol providers emerges, that's new scope for a different ticket, not something GRAPH-001 grows into.
- No specific backing implementation (SCIP, LSP-derived symbol info, CodebaseMemory, GitNexus, etc.) is chosen, evaluated, or built here — that choice belongs to GRAPH-002 (or whoever implements a concrete provider), per the scratch doc §14.2: "Start with one... Choose the one that gives the cleanest deterministic symbol identity with the least integration work."
- No caching layer, no batch-resolution API, no streaming/watch mode — one call site in, one `ExternalSymbolRef` (or "unresolved") out. If a provider implementation needs internal caching to be fast, that's the implementation's concern, not part of this interface's contract.
- No language-specific call-site representation (no `ast.CallExpr`, no Go-specific types) — `CallSite` is a plain, ecosystem-agnostic value so the interface isn't accidentally Go-only.

## Simplicity constraints
- One interface, one method. Not a family of interfaces for different query shapes (e.g. no separate "resolve by position" vs "resolve by name" methods) — a single `Resolve` call takes everything a provider needs.
- Package: `internal/symbolgraph` (new, small — this ticket adds only the interface, the struct, and a `CallSite` input type; no provider implementation lives here).

## Design
Package: `internal/symbolgraph`

```go
// ExternalSymbolRef identifies a symbol defined outside the project
// being analyzed — the target of a call site that resolves into some
// dependency's code, not the calling project's own source.
type ExternalSymbolRef struct {
    Ecosystem     string // e.g. "go", matches domain.Ecosystem's string values
    Module        string // e.g. "go.etcd.io/bbolt"
    Package       string // e.g. "bbolt" (import-path-relative package name)
    QualifiedName string // e.g. "(*Tx).Bucket"
    SourceFile    string // the symbol's defining file, if the provider can report one; "" if unknown
}

// CallSite identifies one place in project source code that a
// SymbolProvider is asked to resolve — file + position is enough for
// any LSP-like provider to answer; no ragctl-specific project state is
// threaded through it.
type CallSite struct {
    File   string // path relative to the project root
    Line   int    // 1-indexed
    Column int    // 1-indexed
}

// SymbolProvider resolves one call site to the external symbol it
// refers to. Implementations back this with whatever graph/LSP/symbol
// index they choose (SCIP, gopls, CodebaseMemory, GitNexus, ...) —
// ragctl only depends on this interface, never on a specific provider.
type SymbolProvider interface {
    // Resolve returns the ExternalSymbolRef the call site refers to, or
    // (ExternalSymbolRef{}, false, nil) if the call site resolves to
    // something internal to the project (not an external dependency) or
    // the provider simply doesn't know. A non-nil error is reserved for
    // the provider itself failing (e.g. its backing index is
    // unreachable), distinct from "resolved to nothing."
    Resolve(ctx context.Context, site CallSite) (ref ExternalSymbolRef, ok bool, err error)
}
```

## Inputs / Outputs
- Input: one `CallSite` (file + line/column within the project being analyzed).
- Output: `(ExternalSymbolRef, ok, error)` — `ok == false` means "not an external symbol / provider doesn't know," `error != nil` means the provider itself failed.

## Failure behavior
- A `SymbolProvider` implementation's own error (backing index unavailable, malformed project state, etc.) is returned as `error`, never silently swallowed into `ok == false` — callers (GRAPH-002) need to distinguish "no external symbol here" from "couldn't check."
- This ticket ships no implementation, so there is no runtime failure mode to test beyond the interface contract itself (exercised via a fake/stub `SymbolProvider` in GRAPH-002's tests).

## Tests
- A fake `SymbolProvider` implementation satisfies the interface (compile-time check) and is usable as a drop-in test double for any future consumer — no `internal/symbolgraph`-owned test beyond confirming the types compile and a trivial fake's behavior round-trips through the interface correctly (`ok=false` for internal symbols, populated `ExternalSymbolRef` for external ones, error propagation).

## Acceptance criteria
- [x] `internal/symbolgraph.SymbolProvider`, `ExternalSymbolRef`, and `CallSite` defined exactly as above (or with only additive, non-breaking field additions if real-world provider testing during implementation reveals a genuinely necessary field).
- [x] No backing implementation, no provider selection logic, no caching added in this ticket.
- [x] Package compiles standalone with no dependency on `internal/query`, `internal/control/bbolt`, or `internal/registry` — this interface knows nothing about ragctl's own domain model, only about symbols and call sites.

// Package symbolgraph joins a project source call site to the external
// dependency symbol it refers to, so ragctl's exact-version knowledge can
// be looked up by "what is this code calling" instead of requiring a
// caller to already know the dependency name (GRAPH-001/002, epic 42).
package symbolgraph

import "context"

// SymbolProvider resolves one call site to the external symbol it refers
// to. Implementations back this with whatever graph/LSP/symbol index
// they choose (go/packages type info, SCIP, gopls, ...) — ragctl only
// depends on this interface, never on a specific provider.
type SymbolProvider interface {
	// Resolve returns the ExternalSymbolRef the call site refers to, or
	// (ExternalSymbolRef{}, false, nil) if the call site resolves to
	// something internal to the project (not an external dependency) or
	// the provider simply doesn't know. A non-nil error is reserved for
	// the provider itself failing (e.g. its backing index is
	// unreachable), distinct from "resolved to nothing."
	Resolve(ctx context.Context, site CallSite) (ref ExternalSymbolRef, ok bool, err error)
}

// CallSite identifies one place in project source code that a
// SymbolProvider is asked to resolve — file + position is enough for any
// LSP-like provider to answer; no ragctl-specific project state is
// threaded through it.
type CallSite struct {
	File   string // path relative to the project root
	Line   int    // 1-indexed
	Column int    // 1-indexed
}

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

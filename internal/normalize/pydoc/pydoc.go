// Package pydoc implements ragctl's Python static signature/docstring
// normalizer (NORM-009): structured, per-symbol API documentation
// extracted from a Python package's own source — the Python analog of
// internal/normalize/godoc and internal/normalize/tsdoc, syntactic
// rather than semantic, and read purely statically. The target
// package's code is never imported or executed, under any circumstance
// — the central constraint this ticket exists to satisfy, not a v1
// shortcut to revisit later (Sphinx/autodoc/pydoc both import the
// target module to introspect it, which is unacceptable for a private
// or unfamiliar dependency ragctl doesn't control).
//
// Extraction runs as a small, dependency-free Python script embedded in
// this package (extract.py) — stdlib `ast` only, no third-party Python
// package (no griffe, matching godoc's own "stdlib only" precedent
// applied to Python's own stdlib instead of Go's). The only new
// operational requirement is `python3` itself on PATH; its absence is a
// clean, silent skip (Supports returns false), not a sync failure.
package pydoc

import (
	"os/exec"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalizer implements normalize.Normalizer for a Python package
// directory. Like godoc.Normalizer and tsdoc.Normalizer, it operates
// per-directory (a package root), invoked directly by
// generation.normalizeSources rather than through normalize.Registry's
// per-file dispatch.
type Normalizer struct{}

// New returns a ready-to-use Python static-extraction normalizer.
func New() *Normalizer {
	return &Normalizer{}
}

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "pydoc-normalizer" }

// Version identifies this normalizer's extraction logic revision.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src points at a Python package directory with
// a resolvable entry module, with `python3` available on PATH.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool {
	if _, err := exec.LookPath("python3"); err != nil {
		return false
	}
	_, ok := entryModuleFile(src.LocalPath)
	return ok
}

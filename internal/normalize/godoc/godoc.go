// Package godoc implements depctl's Go source documentation normalizer
// (NORM-004): package docs and exported symbol documentation extracted
// via the standard library's go/parser and go/doc, not full code search.
// Unexported symbols and full implementation bodies are never indexed —
// depctl is a dependency-documentation lifecycle tool, not a code search
// engine.
package godoc

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// Normalizer implements normalize.Normalizer for a Go package directory.
type Normalizer struct{}

// New returns a ready-to-use Go doc normalizer.
func New() *Normalizer {
	return &Normalizer{}
}

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "godoc-normalizer" }

// Version identifies this normalizer's extraction logic revision.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src points at a directory containing at
// least one .go file — Go doc extraction operates per-package, not
// per-file.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool {
	entries, err := os.ReadDir(src.LocalPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// importPath derives a best-effort import path from a SourceSnapshot
// when the caller hasn't set src.Metadata["import_path"] explicitly —
// falling back to the directory's base name, which is enough for doc.New
// (it only affects rendered examples' package-qualifier, not extraction
// correctness).
func importPath(src domain.SourceSnapshot) string {
	if p := src.Metadata["import_path"]; p != "" {
		return p
	}
	if src.LogicalPath != "" {
		return src.LogicalPath
	}
	return filepath.Base(src.LocalPath)
}

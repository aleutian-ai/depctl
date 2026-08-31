// Package plaintext implements ragctl's plain-text normalizer
// (NORM-003): the simplest normalizer, producing one KnowledgeObject per
// file with no structure extraction beyond trimming. Handles .txt, .rst
// (as plain text — no reStructuredText parsing), and LICENSE-like files
// when explicitly opted in.
package plaintext

import (
	"path/filepath"
	"strings"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalizer implements normalize.Normalizer for .txt/.rst files and,
// when opted in via a SourceSnapshot metadata hint, LICENSE-like files.
type Normalizer struct{}

// New returns a ready-to-use plain-text normalizer.
func New() *Normalizer {
	return &Normalizer{}
}

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "plaintext-normalizer" }

// Version identifies this normalizer's extraction logic revision.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src is a .txt/.rst file, or a LICENSE-like
// file with src.Metadata["include_license"] == "true" — license text is
// skipped by default since it's rarely useful retrieval content.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool {
	lower := strings.ToLower(src.LogicalPath)
	if strings.HasSuffix(lower, ".txt") || strings.HasSuffix(lower, ".rst") {
		return true
	}

	base := strings.ToLower(filepath.Base(src.LogicalPath))
	if strings.HasPrefix(base, "license") {
		return src.Metadata["include_license"] == "true"
	}
	return false
}

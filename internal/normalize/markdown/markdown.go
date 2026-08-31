// Package markdown implements ragctl's Markdown/MDX normalizer
// (NORM-002): syntactic extraction of title, heading hierarchy, fenced
// code-block languages, and links from a document, using goldmark for
// parsing. It never renders HTML or executes MDX components — an .mdx
// file is parsed as plain Markdown, with JSX-looking blocks passed
// through as opaque text.
package markdown

import (
	"strings"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalizer implements normalize.Normalizer for .md and .mdx files.
type Normalizer struct{}

// New returns a ready-to-use Markdown normalizer.
func New() *Normalizer {
	return &Normalizer{}
}

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "markdown-normalizer" }

// Version identifies this normalizer's extraction logic revision; bump
// it whenever extraction behavior changes so previously normalized
// content is flagged for re-normalization.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src is a .md or .mdx file.
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool {
	lower := strings.ToLower(src.LogicalPath)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".mdx")
}

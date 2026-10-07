// Package releasenotes implements ragctl's release-note normalizer
// (NORM-005): a thin decorator over the Markdown and plain-text
// normalizers, not a parser of its own. It tags content from recognized
// release-note sources with content_type=release_note and, for Markdown
// sources, sets release_version from the first heading that looks like a
// version (usually the newest entry).
package releasenotes

import (
	"path/filepath"
	"regexp"
	"strings"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize/markdown"
	"aleutian-ai/ragctl/internal/normalize/plaintext"
)

// knownFilenames are recognized release-note filenames, matched
// case-insensitively against a SourceSnapshot's base LogicalPath.
var knownFilenames = map[string]bool{
	"changelog.md":  true,
	"changes.md":    true,
	"releases.md":   true,
	"history.md":    true,
	"changelog.txt": true,
	"changes.txt":   true,
	"releases.txt":  true,
	"history.txt":   true,
}

// versionHeading matches a heading whose text starts with a semver-ish
// version, optionally prefixed with "v" (e.g. "v1.2.3", "2.0.0 (2024-01-01)").
var versionHeading = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// Normalizer implements normalize.Normalizer for recognized
// release-notes sources, delegating actual parsing to markdown.Normalizer
// or plaintext.Normalizer based on file extension.
type Normalizer struct {
	markdown *markdown.Normalizer
	plain    *plaintext.Normalizer
}

// New returns a ready-to-use release-note normalizer.
func New() *Normalizer {
	return &Normalizer{markdown: markdown.New(), plain: plaintext.New()}
}

// Name identifies this normalizer for content-identity fingerprinting.
func (n *Normalizer) Name() string { return "release-note-normalizer" }

// Version identifies this normalizer's tagging logic revision.
func (n *Normalizer) Version() string { return "v1" }

// Supports reports whether src is a recognized release-notes filename
// (CHANGELOG.md, CHANGES.md, RELEASES.md, HISTORY.md, case-insensitive,
// with either a .md or .txt extension) or is hinted as one via
// src.Metadata["source_type"] == "github-releases".
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool {
	if src.Metadata["source_type"] == "github-releases" {
		return true
	}
	base := strings.ToLower(filepath.Base(src.LogicalPath))
	return knownFilenames[base]
}

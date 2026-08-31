package releasenotes

import (
	"context"
	"strings"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalize delegates to markdown.Normalizer or plaintext.Normalizer
// based on src's extension (or the github-releases hint, since GitHub
// release bodies are Markdown-formatted), then tags every returned
// object content_type=release_note.
//
// NORM-002's markdown normalizer emits one KnowledgeObject per document,
// not per heading section (section-splitting is deferred to CHUNK-002),
// so there is exactly one object to tag here even for a multi-version
// changelog — release_version is set to the first version-like heading
// found (typically the newest entry in a changelog ordered newest-first).
// The full heading breadcrumb list stays available in
// Metadata["headings"] for CHUNK-002 to later derive a release_version
// per section once splitting exists.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	lower := strings.ToLower(src.LogicalPath)
	useMarkdown := strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".mdx") || src.Metadata["source_type"] == "github-releases"

	var objs []domain.KnowledgeObject
	var err error
	if useMarkdown {
		objs, err = n.markdown.Normalize(ctx, src)
	} else {
		objs, err = n.plain.Normalize(ctx, src)
	}
	if err != nil {
		return nil, err
	}

	for i := range objs {
		if objs[i].Metadata == nil {
			objs[i].Metadata = map[string]string{}
		}
		objs[i].Metadata["content_type"] = "release_note"

		if version := firstVersionHeading(objs[i].Metadata["headings"]); version != "" {
			objs[i].Metadata["release_version"] = version
		}
	}
	return objs, nil
}

// firstVersionHeading scans a NORM-002 "headings" metadata value
// (pipe-separated breadcrumb paths, each " > "-joined) for the first
// heading whose deepest segment looks like a version, returning that
// version string, or "" if none match.
func firstVersionHeading(headings string) string {
	if headings == "" {
		return ""
	}
	for _, path := range strings.Split(headings, "|") {
		segments := strings.Split(path, " > ")
		last := segments[len(segments)-1]
		if m := versionHeading.FindString(last); m != "" {
			return m
		}
	}
	return ""
}

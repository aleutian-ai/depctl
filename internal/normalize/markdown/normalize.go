package markdown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalize reads src's materialized file and extracts title, heading
// hierarchy, fenced-code languages, and links into a single
// KnowledgeObject's metadata. Splitting a document into one object per
// heading section is CHUNK-002's job, not this normalizer's — NORM-002
// only needs to preserve the structure for chunking to use later.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	raw, err := os.ReadFile(src.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("markdown: read %s: %w", src.LocalPath, err)
	}

	extracted, err := extract(raw)
	if err != nil {
		return nil, fmt.Errorf("markdown: parse %s: %w", src.LocalPath, err)
	}

	title := extracted.title
	if title == "" {
		title = filepath.Base(src.LogicalPath)
	}

	metadata := map[string]string{}
	if len(extracted.headings) > 0 {
		metadata["headings"] = strings.Join(extracted.headings, "|")
	}
	if len(extracted.languages) > 0 {
		metadata["code_languages"] = strings.Join(extracted.languages, ",")
	}
	if len(extracted.links) > 0 {
		metadata["links"] = strings.Join(extracted.links, ",")
	}
	if strings.TrimSpace(string(raw)) == "" {
		metadata["parse_warning"] = "true"
	}

	obj := domain.KnowledgeObject{
		SourceID:    src.SourceID,
		SourceURI:   src.URI,
		ContentType: "markdown",
		LogicalPath: src.LogicalPath,
		Title:       title,
		Version:     src.Version,
		Commit:      src.Commit,
		Content:     raw,
		Metadata:    metadata,
	}
	return []domain.KnowledgeObject{obj}, nil
}

// extraction is the AST-derived data pulled from one Markdown document.
type extraction struct {
	title     string
	headings  []string // heading breadcrumb paths, e.g. "Getting Started > Installation"
	languages []string // distinct fenced-code-block languages, sorted
	links     []string // link/autolink destinations, in document order
}

// extract walks source's goldmark AST once, collecting title, heading
// hierarchy, fenced-code languages, and links.
func extract(source []byte) (extraction, error) {
	root := goldmark.New().Parser().Parse(text.NewReader(source))

	var result extraction
	var headingStack []string
	languages := map[string]struct{}{}

	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch node.Kind() {
		case ast.KindHeading:
			h := node.(*ast.Heading)
			level := h.Level
			if level-1 > len(headingStack) {
				// A level skip (e.g. H1 straight to H3) — treat as one
				// deeper than the current stack rather than erroring.
				level = len(headingStack) + 1
			}
			headingStack = append(append([]string{}, headingStack[:level-1]...), nodeText(h, source))
			path := strings.Join(headingStack, " > ")
			result.headings = append(result.headings, path)
			if level == 1 && result.title == "" {
				result.title = nodeText(h, source)
			}
		case ast.KindFencedCodeBlock:
			fcb := node.(*ast.FencedCodeBlock)
			if lang := fcb.Language(source); len(lang) > 0 {
				languages[string(lang)] = struct{}{}
			}
		case ast.KindLink:
			l := node.(*ast.Link)
			result.links = append(result.links, string(l.Destination))
		case ast.KindAutoLink:
			l := node.(*ast.AutoLink)
			result.links = append(result.links, string(l.URL(source)))
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return extraction{}, err
	}

	if len(languages) > 0 {
		result.languages = make([]string, 0, len(languages))
		for lang := range languages {
			result.languages = append(result.languages, lang)
		}
		sort.Strings(result.languages)
	}
	return result, nil
}

// nodeText concatenates the plain-text content of node's inline
// descendants — used to pull a heading's text out of its AST children.
func nodeText(node ast.Node, source []byte) string {
	var sb strings.Builder
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			sb.Write(t.Segment.Value(source))
			continue
		}
		sb.WriteString(nodeText(c, source))
	}
	return sb.String()
}

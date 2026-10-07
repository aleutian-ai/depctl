// Command normalize-preview runs ragctl's normalization pipeline
// (internal/normalize) against a single real file or Go package
// directory and prints the resulting KnowledgeObject(s) as JSON. It's a
// dev tool for eyeballing normalizer output against real content (e.g.
// anything under ~/offline-knowledge) without running a full sync.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize"
	"aleutian-ai/ragctl/internal/normalize/godoc"
	"aleutian-ai/ragctl/internal/normalize/markdown"
	"aleutian-ai/ragctl/internal/normalize/plaintext"
	"aleutian-ai/ragctl/internal/normalize/releasenotes"
)

// maxContentPreview bounds how much of a KnowledgeObject's Content this
// tool prints, so previewing e.g. python/cpython's full doc tree doesn't
// flood the terminal.
const maxContentPreview = 2000

func main() {
	path := flag.String("path", "", "file to normalize, or a Go package directory")
	includeLicense := flag.Bool("include-license", false, "opt in to normalizing a LICENSE-like file (plaintext normalizer)")
	flag.Parse()

	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: normalize-preview -path <file-or-package-dir>")
		os.Exit(2)
	}

	abs, err := filepath.Abs(*path)
	if err != nil {
		log.Fatalf("abs path: %v", err)
	}

	metadata := map[string]string{}
	if *includeLicense {
		metadata["include_license"] = "true"
	}

	src := domain.SourceSnapshot{
		LocalPath:   abs,
		LogicalPath: filepath.Base(abs),
		Metadata:    metadata,
	}

	// releasenotes must come before markdown/plaintext: a CHANGELOG.md
	// matches both, and the more specific normalizer should win.
	registry := normalize.NewRegistry(
		releasenotes.New(),
		markdown.New(),
		plaintext.New(),
		godoc.New(),
	)

	n, ok := registry.Select(src)
	if !ok {
		fmt.Fprintf(os.Stderr, "no normalizer supports %s\n", *path)
		os.Exit(1)
	}

	objects, err := n.Normalize(context.Background(), src)
	if err != nil {
		log.Fatalf("normalize: %v", err)
	}

	fmt.Fprintf(os.Stderr, "normalizer: %s %s (%d object(s))\n\n", n.Name(), n.Version(), len(objects))

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, obj := range objects {
		if err := enc.Encode(preview(obj)); err != nil {
			log.Fatalf("encode: %v", err)
		}
	}
}

// previewObject is a KnowledgeObject with Content truncated for
// terminal-friendly printing.
type previewObject struct {
	Title       string            `json:"title"`
	ContentType string            `json:"content_type"`
	LogicalPath string            `json:"logical_path"`
	Language    string            `json:"language,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	ContentSize int               `json:"content_bytes"`
	Content     string            `json:"content"`
	Truncated   bool              `json:"truncated,omitempty"`
}

func preview(o domain.KnowledgeObject) previewObject {
	content := string(o.Content)
	truncated := false
	if len(content) > maxContentPreview {
		content = content[:maxContentPreview]
		truncated = true
	}
	return previewObject{
		Title:       o.Title,
		ContentType: o.ContentType,
		LogicalPath: o.LogicalPath,
		Language:    o.Language,
		Metadata:    o.Metadata,
		ContentSize: len(o.Content),
		Content:     content,
		Truncated:   truncated,
	}
}

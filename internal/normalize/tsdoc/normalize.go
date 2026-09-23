package tsdoc

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize"
)

//go:embed extract.js
var extractScript []byte

// extractedSymbol is one exported declaration extract.js reports.
type extractedSymbol struct {
	Kind      string `json:"kind"` // "function" | "class" | "method" | "interface" | "type" | "const"
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Doc       string `json:"doc"`
	Receiver  string `json:"receiver,omitempty"` // set for a class method
}

// extractedModule is extract.js's whole-file output.
type extractedModule struct {
	Doc     string             `json:"doc"`
	Symbols []extractedSymbol  `json:"symbols"`
}

// Normalize extracts src.LocalPath's Node package's published .d.ts
// entry point into one package_doc object (the file's own leading
// comment, if any) plus one symbol_doc object per exported declaration.
// A dependency with no resolvable entry point, or no `node` on PATH,
// never reaches here — Supports already returned false for both. A
// `node` present but the embedded extract.js itself failing to run is
// distinct from either: a ragctl packaging defect, not this dependency's
// own documentation gap, so it's returned as a real error rather than
// silently producing zero objects.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	entry, ok := entryDeclarationFile(src.LocalPath)
	if !ok {
		return nil, nil
	}

	mod, err := runExtract(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("tsdoc: %s: %w", entry, err)
	}

	pkgName := filepath.Base(src.LocalPath)
	var objects []domain.KnowledgeObject
	if mod.Doc != "" {
		objects = append(objects, domain.KnowledgeObject{
			SourceID:    src.SourceID,
			SourceURI:   src.URI,
			ContentType: "package_doc",
			LogicalPath: src.LogicalPath,
			Title:       pkgName,
			Version:     src.Version,
			Commit:      src.Commit,
			Content:     normalize.NormalizeLineEndings([]byte(mod.Doc)),
			Metadata:    map[string]string{"package": pkgName},
		})
	}
	for _, sym := range mod.Symbols {
		metadata := map[string]string{"symbol": sym.Name, "kind": sym.Kind}
		if sym.Signature != "" {
			metadata["signature"] = sym.Signature
		}
		if sym.Receiver != "" {
			metadata["receiver"] = sym.Receiver
		}
		objects = append(objects, domain.KnowledgeObject{
			SourceID:    src.SourceID,
			SourceURI:   src.URI,
			ContentType: "symbol_doc",
			LogicalPath: src.LogicalPath,
			Title:       sym.Name,
			Version:     src.Version,
			Commit:      src.Commit,
			Content:     normalize.NormalizeLineEndings([]byte(sym.Doc)),
			Metadata:    metadata,
		})
	}
	return objects, nil
}

// runExtract runs the embedded extract.js against entry via a `node`
// subprocess, piping the script itself over stdin rather than writing it
// to a temp file — no filesystem cleanup needed, and it can never be
// confused with, or accidentally read as, a file already in the
// dependency's own checked-out worktree.
func runExtract(ctx context.Context, entry string) (extractedModule, error) {
	var mod extractedModule
	cmd := exec.CommandContext(ctx, "node", "--input-type=commonjs", "-", entry)
	cmd.Stdin = bytes.NewReader(extractScript)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return mod, fmt.Errorf("run extract.js: %w: %s", err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &mod); err != nil {
		return mod, fmt.Errorf("parse extract.js output: %w", err)
	}
	return mod, nil
}

package pydoc

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

//go:embed extract.py
var extractScript []byte

// extractedSymbol is one resolved public symbol extract.py reports.
type extractedSymbol struct {
	Kind      string `json:"kind"` // "function" | "class" | "method"
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Doc       string `json:"doc"`
	Receiver  string `json:"receiver,omitempty"` // set for a class method
}

// extractedModule is extract.py's whole-module output.
type extractedModule struct {
	Doc            string            `json:"doc"`
	Symbols        []extractedSymbol `json:"symbols"`
	ExportsDynamic bool              `json:"exportsDynamic"`
}

// Normalize extracts src.LocalPath's Python package's entry module into
// one package_doc object (the module's own docstring) plus one
// symbol_doc object per resolved public symbol. A dependency with no
// resolvable entry module, or no `python3` on PATH, never reaches here —
// Supports already returned false for both. `python3` present but the
// embedded extract.py itself failing to run is a ragctl packaging
// defect, not this dependency's own gap — returned as a real error.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	entry, ok := entryModuleFile(src.LocalPath)
	if !ok {
		return nil, nil
	}

	mod, err := runExtract(ctx, entry, src.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("pydoc: %s: %w", entry, err)
	}

	pkgName := filepath.Base(src.LocalPath)
	var objects []domain.KnowledgeObject
	if mod.Doc != "" || mod.ExportsDynamic {
		metadata := map[string]string{"package": pkgName}
		if mod.ExportsDynamic {
			// __all__ exists but isn't a statically-literal list — the
			// symbol set below is a best-effort convention-based guess,
			// not a verified-complete public API. Recorded explicitly so
			// an agent (or a person) can tell "this is everything" from
			// "this package computes its exports dynamically."
			metadata["exports_dynamic"] = "true"
		}
		objects = append(objects, domain.KnowledgeObject{
			SourceID:    src.SourceID,
			SourceURI:   src.URI,
			ContentType: "package_doc",
			LogicalPath: src.LogicalPath,
			Title:       pkgName,
			Version:     src.Version,
			Commit:      src.Commit,
			Content:     normalize.NormalizeLineEndings([]byte(mod.Doc)),
			Metadata:    metadata,
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

// runExtract runs the embedded extract.py against entry (and pkgDir, for
// resolving a single-hop relative-import re-export) via a `python3`
// subprocess, piping the script itself over stdin — no filesystem
// cleanup needed, and it can never be confused with a file already in
// the dependency's own checked-out worktree.
func runExtract(ctx context.Context, entry, pkgDir string) (extractedModule, error) {
	var mod extractedModule
	cmd := exec.CommandContext(ctx, "python3", "-", entry, pkgDir)
	cmd.Stdin = bytes.NewReader(extractScript)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return mod, fmt.Errorf("run extract.py: %w: %s", err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &mod); err != nil {
		return mod, fmt.Errorf("parse extract.py output: %w", err)
	}
	return mod, nil
}

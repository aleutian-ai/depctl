package tsdoc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
}

const goldenDTS = `/**
 * The widget package does something useful.
 */

/**
 * Do performs the widget's core action.
 * @param x the input
 */
export declare function Do(x: string): number;

/**
 * Widget is the package's main type.
 */
export declare class Widget {
    /**
     * Run executes the widget.
     */
    run(count: number): void;

    private helper(): void;
}

/** Options configures a Widget. */
export interface Options {
    name: string;
}

/** internalHelper is not exported and must never appear. */
declare function internalHelper(): void;
`

func TestExtractGoldenFixtureDTS(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "index.d.ts")
	if err := os.WriteFile(path, []byte(goldenDTS), 0o644); err != nil {
		t.Fatal(err)
	}

	mod, err := runExtract(context.Background(), path)
	if err != nil {
		t.Fatalf("runExtract: %v", err)
	}

	if !strings.Contains(mod.Doc, "widget package does something useful") {
		t.Errorf("module doc = %q, want the file's leading comment", mod.Doc)
	}

	byName := map[string]extractedSymbol{}
	for _, s := range mod.Symbols {
		byName[s.Name] = s
	}

	if _, ok := byName["internalHelper"]; ok {
		t.Error("internalHelper (not exported) must never appear")
	}
	if _, ok := byName["helper"]; ok {
		t.Error("Widget.helper (private) must never appear")
	}

	do, ok := byName["Do"]
	if !ok {
		t.Fatal("Do function missing")
	}
	if do.Kind != "function" || !strings.Contains(do.Doc, "performs the widget's core action") || !strings.Contains(do.Signature, "function Do(x: string): number") {
		t.Errorf("Do = %+v", do)
	}

	widget, ok := byName["Widget"]
	if !ok || widget.Kind != "class" || !strings.Contains(widget.Doc, "package's main type") {
		t.Errorf("Widget = %+v, ok=%v", widget, ok)
	}

	run, ok := byName["run"]
	if !ok || run.Kind != "method" || run.Receiver != "Widget" || !strings.Contains(run.Doc, "executes the widget") {
		t.Errorf("run = %+v, ok=%v", run, ok)
	}

	opts, ok := byName["Options"]
	if !ok || opts.Kind != "interface" || !strings.Contains(opts.Doc, "configures a Widget") {
		t.Errorf("Options = %+v, ok=%v", opts, ok)
	}

	wantCount := 4 // Do, Widget, run, Options — helper and internalHelper excluded
	if len(mod.Symbols) != wantCount {
		t.Errorf("len(Symbols) = %d, want %d: %+v", len(mod.Symbols), wantCount, mod.Symbols)
	}
}

func TestSupportsFalseWithoutNode(t *testing.T) {
	// This test doesn't require asserting node's actual presence either
	// way — it only checks that entryDeclarationFile itself is exercised
	// correctly regardless, via a directory with a real .d.ts.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.d.ts"), []byte("export declare function f(): void;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := domain.SourceSnapshot{LocalPath: dir}
	n := New()
	got := n.Supports(src)
	_, nodeErr := exec.LookPath("node")
	want := nodeErr == nil
	if got != want {
		t.Errorf("Supports = %v, want %v (node present = %v)", got, want, nodeErr == nil)
	}
}

func TestEntryDeclarationFilePrefersPackageJSONTypesField(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"types": "dist/index.d.ts"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "index.d.ts"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// A decoy at the root that must NOT be picked when "types" resolves.
	if err := os.WriteFile(filepath.Join(dir, "other.d.ts"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := entryDeclarationFile(dir)
	want := filepath.Join(dir, "dist", "index.d.ts")
	if !ok || got != want {
		t.Errorf("entryDeclarationFile = %q, %v, want %q, true", got, ok, want)
	}
}

func TestEntryDeclarationFileNoneFound(t *testing.T) {
	dir := t.TempDir()
	if _, ok := entryDeclarationFile(dir); ok {
		t.Error("entryDeclarationFile found something in an empty directory")
	}
}

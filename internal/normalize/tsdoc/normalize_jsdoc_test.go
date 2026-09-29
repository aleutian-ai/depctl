package tsdoc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

// goldenJS mirrors goldenDTS's shape exactly (same symbol names/roles:
// a module doc, a documented exported function, a documented exported
// class with one public and one private method, a private/unexported
// symbol that must never appear) but through JSDoc-over-plain-.js —
// NORM-008's deferred fallback scope, closed here — and adds the two
// CommonJS assignment forms real npm packages commonly use alongside or
// instead of ES module syntax, plus a sibling directory (a second
// package in the same worktree) whose symbols must never leak in,
// mirroring epic 56/REG-012's own monorepo-sibling-leak regression
// shape at the normalizer level.
const goldenJS = `/**
 * The widget package does something useful.
 */

/**
 * Do performs the widget's core action.
 * @param {string} x the input
 * @returns {number}
 */
export function Do(x) {
  return x.length;
}

/**
 * Widget is the package's main type.
 */
export class Widget {
  /**
   * Run executes the widget.
   */
  run(count) {
    return count;
  }

  #helper() {
    return 0;
  }
}

/**
 * Scale is a CommonJS function export.
 * @param {number} n
 */
exports.Scale = function(n) {
  return n * 2;
};

/**
 * Gadget is a CommonJS class export.
 */
module.exports.Gadget = class {
  spin() {
    return true;
  }
};

function internalHelper() {
  return 0;
}
`

func TestExtractGoldenFixtureJSDoc(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "index.js")
	if err := os.WriteFile(path, []byte(goldenJS), 0o644); err != nil {
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
		t.Error("Widget's private #helper must never appear")
	}

	do, ok := byName["Do"]
	if !ok {
		t.Fatal("Do function missing")
	}
	if do.Kind != "function" || !strings.Contains(do.Doc, "performs the widget's core action") || !strings.Contains(do.Doc, "@param {string} x") || !strings.Contains(do.Signature, "export function Do(x)") {
		t.Errorf("Do = %+v", do)
	}
	if strings.Contains(do.Signature, "x.length") {
		t.Errorf("Do.Signature = %q, want the implementation body truncated away", do.Signature)
	}

	widget, ok := byName["Widget"]
	if !ok || widget.Kind != "class" || !strings.Contains(widget.Doc, "package's main type") {
		t.Errorf("Widget = %+v, ok=%v", widget, ok)
	}

	run, ok := byName["run"]
	if !ok || run.Kind != "method" || run.Receiver != "Widget" || !strings.Contains(run.Doc, "executes the widget") {
		t.Errorf("run = %+v, ok=%v", run, ok)
	}

	scale, ok := byName["Scale"]
	if !ok || scale.Kind != "function" || !strings.Contains(scale.Doc, "CommonJS function export") || !strings.Contains(scale.Signature, "exports.Scale = function(n)") {
		t.Errorf("Scale (CommonJS function export) = %+v, ok=%v", scale, ok)
	}

	gadget, ok := byName["Gadget"]
	if !ok || gadget.Kind != "class" || !strings.Contains(gadget.Doc, "CommonJS class export") {
		t.Errorf("Gadget (CommonJS class export) = %+v, ok=%v", gadget, ok)
	}
	spin, ok := byName["spin"]
	if !ok || spin.Kind != "method" || spin.Receiver != "Gadget" {
		t.Errorf("Gadget.spin = %+v, ok=%v", spin, ok)
	}

	wantCount := 6 // Do, Widget, run, Scale, Gadget, spin
	if len(mod.Symbols) != wantCount {
		t.Errorf("len(Symbols) = %d, want %d: %+v", len(mod.Symbols), wantCount, mod.Symbols)
	}
}

// TestExtractGoldenFixtureJSDocSiblingNeverLeaks mirrors epic 56/REG-012's
// monorepo-sibling-leak regression shape at the extraction level: the
// JSDoc fallback only ever runs against the one resolved entry file
// (resolveEntry/entryJSFile), never a directory walk, so a second
// package's file sitting next to it in the same worktree is never
// touched — proven here by pointing runExtract only at Do's own file
// and confirming the sibling's own exports are absent, not by testing
// resolveEntry's directory scoping again (already covered elsewhere).
func TestExtractGoldenFixtureJSDocSiblingNeverLeaks(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	ownPath := filepath.Join(dir, "index.js")
	if err := os.WriteFile(ownPath, []byte(goldenJS), 0o644); err != nil {
		t.Fatal(err)
	}
	siblingDir := filepath.Join(filepath.Dir(dir), "sibling-pkg")
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siblingDir, "index.js"), []byte("/** Should never appear. */\nexport function SiblingOnly() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mod, err := runExtract(context.Background(), ownPath)
	if err != nil {
		t.Fatalf("runExtract: %v", err)
	}
	for _, s := range mod.Symbols {
		if s.Name == "SiblingOnly" {
			t.Errorf("sibling package's SiblingOnly leaked into this package's extraction: %+v", mod.Symbols)
		}
	}
}

func TestEntryJSFilePrefersModuleThenMainThenIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "esm.js"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"module": "esm.js", "main": "cjs.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// cjs.js deliberately not created — "module" should be preferred and
	// found before "main" is even considered.
	got, ok := entryJSFile(dir)
	want := filepath.Join(dir, "esm.js")
	if !ok || got != want {
		t.Errorf("entryJSFile = %q, %v, want %q, true", got, ok, want)
	}
}

func TestEntryJSFileFallsBackToIndexJS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// No package.json at all.
	got, ok := entryJSFile(dir)
	want := filepath.Join(dir, "index.js")
	if !ok || got != want {
		t.Errorf("entryJSFile = %q, %v, want %q, true", got, ok, want)
	}
}

func TestEntryJSFileResolvesExtensionlessMain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.js"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"main": "lib"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := entryJSFile(dir)
	want := filepath.Join(dir, "lib.js")
	if !ok || got != want {
		t.Errorf("entryJSFile = %q, %v, want %q, true", got, ok, want)
	}
}

func TestResolveEntryPrefersDTSOverJS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.d.ts"), []byte("export declare function f(): void;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("export function f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, isJS, ok := resolveEntry(dir)
	if !ok || isJS || path != filepath.Join(dir, "index.d.ts") {
		t.Errorf("resolveEntry = %q, isJS=%v, ok=%v, want the .d.ts, isJS=false", path, isJS, ok)
	}
}

func TestResolveEntryFallsBackToJSDocWhenNoDTS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("export function f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, isJS, ok := resolveEntry(dir)
	if !ok || !isJS || path != filepath.Join(dir, "index.js") {
		t.Errorf("resolveEntry = %q, isJS=%v, ok=%v, want the .js fallback, isJS=true", path, isJS, ok)
	}
}

// TestNormalizePackageDocTitleUsesPackageJSONNameNotWorktreeDirName is a
// live-found regression: package_doc's Title used to be
// filepath.Base(src.LocalPath) — for a package with no Subdir scoping,
// src.LocalPath IS the worktree's own randomly-named temp directory
// (confirmed live: real mem0-sourced packages `debug`/`ms` produced a
// package_doc titled literally "ragctl-worktree-1160938559" instead of
// their own name). package.json's "name" field is the one authoritative
// source and is now used instead (packageDisplayName, entrypoint.go).
func TestNormalizePackageDocTitleUsesPackageJSONNameNotWorktreeDirName(t *testing.T) {
	requireNode(t)
	// A directory name deliberately shaped like a real worktree temp dir
	// — filepath.Base(dir) would return exactly this if the old bug were
	// still present.
	base := t.TempDir()
	dir := filepath.Join(base, "ragctl-worktree-1160938559")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "@scope/real-package-name", "main": "index.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("/**\n * The real package's own module doc.\n */\n\n/**\n * f does something.\n */\nexport function f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	n := New()
	objs, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: dir})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	var pkgDoc *domain.KnowledgeObject
	for i := range objs {
		if objs[i].ContentType == "package_doc" {
			pkgDoc = &objs[i]
		}
	}
	if pkgDoc == nil {
		t.Fatal("no package_doc object produced")
	}
	if pkgDoc.Title != "@scope/real-package-name" {
		t.Errorf("package_doc.Title = %q, want the real package.json name %q — not the worktree directory name", pkgDoc.Title, "@scope/real-package-name")
	}
	if pkgDoc.Metadata["package"] != "@scope/real-package-name" {
		t.Errorf("package_doc.Metadata[package] = %q, want %q", pkgDoc.Metadata["package"], "@scope/real-package-name")
	}
}

// TestSupportsTrueForJSDocFallback proves the fallback is actually wired
// into Supports/Normalize's shared entry point, not just directly
// testable in isolation.
func TestSupportsTrueForJSDocFallback(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("export function f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := New()
	if !n.Supports(domain.SourceSnapshot{LocalPath: dir}) {
		t.Error("Supports = false, want true for a directory with a plain .js entry point and no .d.ts")
	}
}

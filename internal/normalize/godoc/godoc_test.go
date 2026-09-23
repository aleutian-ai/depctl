package godoc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func fixtureSnapshot(t *testing.T) domain.SourceSnapshot {
	t.Helper()
	dir := filepath.Join("testdata", "sources", "simplepkg")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	return domain.SourceSnapshot{LocalPath: abs, LogicalPath: "simplepkg"}
}

// TestExternalTestOnlyDirectorySkippedNotErrored covers a directory
// whose only Go files are an external "foo_test" package (no
// importable non-test package) — a normal Go layout (e.g.
// integration-test-only dirs) with nothing to document, not a content
// error. Normalize must return zero objects and a nil error so
// generation.Build (which treats any normalizer error as fatal to the
// whole generation) doesn't abort real syncs over this.
func TestExternalTestOnlyDirectorySkippedNotErrored(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "sources", "testonlypkg"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: dir, LogicalPath: "testonlypkg"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Normalize returned %d objects, want 0", len(got))
	}
}

// TestUnparseableFixtureSkippedNotErrored covers a directory containing
// a .go file that doesn't parse (e.g. golang/tools' cmd/fiximports
// testdata, which intentionally ships invalid Go source to exercise
// error handling). parser.ParseDir aborts on the first such file — not
// evidence of a bug in this normalizer, just a directory with no valid
// documentable package. Skip it rather than failing the whole
// generation.
func TestUnparseableFixtureSkippedNotErrored(t *testing.T) {
	// Written at run time, not checked in under testdata/, so this
	// deliberately-invalid .go file never lands in the working tree for
	// gofmt/go vet's own recursive walk to trip over.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), nil, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: dir, LogicalPath: "unparseable"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Normalize returned %d objects, want 0", len(got))
	}
}

func TestSupportsRequiresGoFiles(t *testing.T) {
	n := New()
	if !n.Supports(fixtureSnapshot(t)) {
		t.Error("Supports(package dir) = false, want true")
	}
	if n.Supports(domain.SourceSnapshot{LocalPath: t.TempDir()}) {
		t.Error("Supports(empty dir) = true, want false")
	}
	if n.Supports(domain.SourceSnapshot{LocalPath: "/does/not/exist"}) {
		t.Error("Supports(nonexistent dir) = true, want false")
	}
}

func normalizeFixture(t *testing.T) []domain.KnowledgeObject {
	t.Helper()
	n := New()
	got, err := n.Normalize(context.Background(), fixtureSnapshot(t))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return got
}

func findByTitle(objs []domain.KnowledgeObject, title, contentType string) (domain.KnowledgeObject, bool) {
	for _, o := range objs {
		if o.Title == title && o.ContentType == contentType {
			return o, true
		}
	}
	return domain.KnowledgeObject{}, false
}

func TestPackageDocCaptured(t *testing.T) {
	objs := normalizeFixture(t)
	obj, ok := findByTitle(objs, "simplepkg", "package_doc")
	if !ok {
		t.Fatal("no package_doc object found")
	}
	if got := string(obj.Content); got == "" {
		t.Error("package doc Content is empty")
	}
}

func TestExportedFunctionLookup(t *testing.T) {
	objs := normalizeFixture(t)
	obj, ok := findByTitle(objs, "Greet", "symbol_doc")
	if !ok {
		t.Fatal("no symbol_doc object found for Greet")
	}
	if obj.Metadata["signature"] == "" {
		t.Error("Greet's signature metadata is empty")
	}
	if obj.Metadata["receiver"] != "" {
		t.Errorf("Greet is a plain function, want no receiver, got %q", obj.Metadata["receiver"])
	}
}

func TestTypeAndMethodDocsLinked(t *testing.T) {
	objs := normalizeFixture(t)

	typeObj, ok := findByTitle(objs, "Config", "symbol_doc")
	if !ok {
		t.Fatal("no symbol_doc object found for Config type")
	}
	if typeObj.Metadata["receiver"] != "" {
		t.Errorf("Config type object should have no receiver, got %q", typeObj.Metadata["receiver"])
	}

	methodObj, ok := findByTitle(objs, "Describe", "symbol_doc")
	if !ok {
		t.Fatal("no symbol_doc object found for Describe method")
	}
	if methodObj.Metadata["receiver"] != "Config" {
		t.Errorf("Describe's receiver = %q, want Config", methodObj.Metadata["receiver"])
	}
}

// TestConstructorFunctionReturningTypeIsExtracted is VALID-004's
// regression test: go/doc groups a package-level function under the
// type it returns (Type.Funcs), not the package-level Doc.Funcs list —
// the extremely common Go constructor convention (NewFoo(), Open(),
// Connect() returning *T). Normalize previously only read Type.Methods,
// silently never indexing any such constructor at all, found via
// VALID-004's live fixture (a Connect(...) (*Client, error) function
// that never appeared in search results).
func TestConstructorFunctionReturningTypeIsExtracted(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "sources", "constructorpkg"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	n := New()
	objs, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: dir, LogicalPath: "constructorpkg"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	obj, ok := findByTitle(objs, "NewClient", "symbol_doc")
	if !ok {
		t.Fatal("no symbol_doc object found for NewClient — Type.Funcs functions are not being extracted")
	}
	if obj.Metadata["receiver"] != "" {
		t.Errorf("NewClient is a package-level function, not a method, want no receiver, got %q", obj.Metadata["receiver"])
	}
	if obj.Metadata["signature"] == "" {
		t.Error("NewClient's signature metadata is empty")
	}
	if string(obj.Content) == "" {
		t.Error("NewClient's doc Content is empty")
	}

	// The method (a genuinely different code path, Type.Methods) must
	// still work correctly alongside the fix.
	methodObj, ok := findByTitle(objs, "Ping", "symbol_doc")
	if !ok {
		t.Fatal("no symbol_doc object found for Ping method")
	}
	if methodObj.Metadata["receiver"] != "Client" {
		t.Errorf("Ping's receiver = %q, want Client", methodObj.Metadata["receiver"])
	}
}

func TestUnexportedSymbolsNeverAppear(t *testing.T) {
	objs := normalizeFixture(t)
	for _, o := range objs {
		if o.Title == "helper" || o.Title == "internalFlag" {
			t.Errorf("unexported symbol %q leaked into output", o.Title)
		}
	}
}

// goldenSymbol is the subset of a symbol_doc/package_doc KnowledgeObject
// compared against the golden fixture — omits Content and the
// go/printer-rendered "signature" metadata, both of which are more
// prone to incidental formatting drift than to the extraction bugs a
// golden test exists to catch.
type goldenSymbol struct {
	Title       string `json:"title"`
	ContentType string `json:"content_type"`
	Receiver    string `json:"receiver,omitempty"`
}

func TestGoldenSnapshot(t *testing.T) {
	objs := normalizeFixture(t)

	got := make([]goldenSymbol, 0, len(objs))
	for _, o := range objs {
		got = append(got, goldenSymbol{Title: o.Title, ContentType: o.ContentType, Receiver: o.Metadata["receiver"]})
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].ContentType != got[j].ContentType {
			return got[i].ContentType < got[j].ContentType
		}
		return got[i].Title < got[j].Title
	})

	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "golden", "simplepkg.golden.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var want []goldenSymbol
	if err := json.Unmarshal(goldenBytes, &want); err != nil {
		t.Fatalf("unmarshal golden fixture: %v", err)
	}

	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("normalized output drifted from golden fixture:\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

// writeConstGroupPackage writes a package whose only content is one
// n-name exported const group, the shape of generated enum/endpoint
// tables in real SDKs.
func writeConstGroupPackage(t *testing.T, n int) domain.SourceSnapshot {
	t.Helper()
	var b strings.Builder
	b.WriteString("// Package enums holds a generated enum table.\npackage enums\n\n// Codes are the generated codes.\nconst (\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\tCode%04d = \"code-%04d\"\n", i, i)
	}
	b.WriteString(")\n")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "enums.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return domain.SourceSnapshot{LocalPath: dir, LogicalPath: "enums"}
}

// TestHugeConstGroupSignaturesStayBounded is the regression for the
// STRESS-005 OOM: go/doc reports one const group as one declaration, and
// every name in it was given that whole declaration as its signature, so
// a group of N names cost N*N text — 2.4 GB live in one dependency's
// build. Each name's signature must stay small regardless of group size.
func TestHugeConstGroupSignaturesStayBounded(t *testing.T) {
	objs, err := New().Normalize(context.Background(), writeConstGroupPackage(t, 1500))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	var symbols, total, largest int
	for _, o := range objs {
		if o.ContentType != "symbol_doc" {
			continue
		}
		symbols++
		n := len(o.Metadata["signature"])
		total += n
		if n > largest {
			largest = n
		}
	}
	if symbols != 1500 {
		t.Fatalf("got %d symbol objects, want 1500 (every constant still indexed)", symbols)
	}
	if largest > maxSignatureBytes*2 {
		t.Errorf("largest signature = %d bytes, want it bounded near %d — the whole group is being repeated per name (total %d bytes)", largest, maxSignatureBytes, total)
	}

	code, ok := findByTitle(objs, "Code0042", "symbol_doc")
	if !ok || !strings.Contains(code.Metadata["signature"], `Code0042 = "code-0042"`) {
		t.Errorf("Code0042 signature = %q, want it to show its own definition", code.Metadata["signature"])
	}
}

// TestSmallConstGroupKeepsWholeBlockSignature: bounded means only big
// groups change — an ordinary enum block still shows its full context
// (the type and the sibling values an iota reader needs).
func TestSmallConstGroupKeepsWholeBlockSignature(t *testing.T) {
	objs, err := New().Normalize(context.Background(), writeConstGroupPackage(t, 5))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	code, ok := findByTitle(objs, "Code0002", "symbol_doc")
	if !ok {
		t.Fatal("Code0002 not found")
	}
	sig := code.Metadata["signature"]
	if !strings.Contains(sig, "Code0000") || !strings.Contains(sig, "Code0004") {
		t.Errorf("small group signature = %q, want the whole block", sig)
	}
}

// TestLibraryWithABuildIgnoredMainGeneratorIsStillDocumented is the
// regression for a silent zero-symbol dependency found by the Ollama smoke
// test (github.com/apparentlymart/go-textseg/v12): a directory can hold the
// real library package plus a build-ignored `package main` generator (a
// very common shape). singlePackage picked the alphabetically-first
// package, so any library whose name sorts after "main" was replaced by its
// own generator, and the dependency indexed nothing.
func TestLibraryWithABuildIgnoredMainGeneratorIsStillDocumented(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"textseg.go":  "// Package textseg splits text into grapheme clusters.\npackage textseg\n\n// Split returns the clusters of s.\nfunc Split(s string) []string { return nil }\n",
		"generate.go": "//go:build ignore\n// +build ignore\n\n// This generator rebuilds the tables.\npackage main\n\n// Generate is exported but is not part of the library.\nfunc Generate() {}\n\nfunc main() {}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	objs, err := New().Normalize(context.Background(), domain.SourceSnapshot{LocalPath: dir, LogicalPath: "textseg"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if _, ok := findByTitle(objs, "Split", "symbol_doc"); !ok {
		t.Errorf("the library's Split is missing; got %d objects, want the real package documented", len(objs))
	}
	if _, ok := findByTitle(objs, "Generate", "symbol_doc"); ok {
		t.Error("the build-ignored generator's Generate was documented as part of the library")
	}
}

// TestCommandDirectoryWithOnlyMainIsStillDocumented: a directory that is
// only a command keeps its main package's docs.
func TestCommandDirectoryWithOnlyMainIsStillDocumented(t *testing.T) {
	dir := t.TempDir()
	src := "// Command tool does a thing.\npackage main\n\n// Run does it.\nfunc Run() {}\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	objs, err := New().Normalize(context.Background(), domain.SourceSnapshot{LocalPath: dir, LogicalPath: "tool"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if _, ok := findByTitle(objs, "Run", "symbol_doc"); !ok {
		t.Errorf("a main-only directory lost its docs; got %d objects", len(objs))
	}
}

package godoc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

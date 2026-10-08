package project

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func buildFixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// canonicalize now resolves symlinks (PROJ-002) — t.TempDir() on
	// macOS returns a path under the symlinked /var (-> /private/var), so
	// resolve it here too or every comparison against Scan's now-resolved
	// dp.Root would see a long, spurious ../../.. relative path instead
	// of the short one this test expects.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	touch(t, filepath.Join(root, "service-go", "go.mod"))
	touch(t, filepath.Join(root, "web", "package.json"))
	touch(t, filepath.Join(root, "tools", "rust-tool", "Cargo.toml"))
	touch(t, filepath.Join(root, "data", "python", "pyproject.toml"))
	// Should never be descended into.
	touch(t, filepath.Join(root, "web", "node_modules", "pkg", "go.mod"))
	touch(t, filepath.Join(root, "service-go", ".git", "go.mod"))
	return root
}

func TestScanDetectsFixtureTree(t *testing.T) {
	root := buildFixtureTree(t)

	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := map[string]domain.Ecosystem{
		"service-go":      domain.EcosystemGo,
		"web":             domain.EcosystemNode,
		"tools/rust-tool": domain.EcosystemRust,
		"data/python":     domain.EcosystemPython,
	}

	if len(got) != len(want) {
		t.Fatalf("got %d detected projects, want %d: %+v", len(got), len(want), got)
	}

	for _, dp := range got {
		rel, err := filepath.Rel(root, dp.Root)
		if err != nil {
			t.Fatalf("Rel: %v", err)
		}
		rel = filepath.ToSlash(rel)
		wantEco, ok := want[rel]
		if !ok {
			t.Errorf("unexpected detected project at %s", rel)
			continue
		}
		if dp.Ecosystem != wantEco {
			t.Errorf("%s: ecosystem = %s, want %s", rel, dp.Ecosystem, wantEco)
		}
	}
}

func TestScanSkipsIgnoredDirs(t *testing.T) {
	root := buildFixtureTree(t)
	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, dp := range got {
		if filepath.Base(filepath.Dir(dp.Root)) == "node_modules" {
			t.Errorf("descended into node_modules: %+v", dp)
		}
	}
}

func TestScanPolyglotDirectoryYieldsMultipleEntries(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "poly", "go.mod"))
	touch(t, filepath.Join(root, "poly", "package.json"))

	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries for polyglot dir, want 2: %+v", len(got), got)
	}
	ecosystems := []string{string(got[0].Ecosystem), string(got[1].Ecosystem)}
	sort.Strings(ecosystems)
	if ecosystems[0] != "go" || ecosystems[1] != "node" {
		t.Errorf("unexpected ecosystems: %v", ecosystems)
	}
	if got[0].Root != got[1].Root {
		t.Errorf("polyglot entries should share the same root: %+v", got)
	}
}

func TestScanPermissionDeniedSubtreeDoesNotAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	root := t.TempDir()
	touch(t, filepath.Join(root, "good", "go.mod"))
	blocked := filepath.Join(root, "blocked")
	touch(t, filepath.Join(blocked, "inner", "go.mod"))
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan returned an error instead of skipping the blocked subtree: %v", err)
	}
	found := false
	for _, dp := range got {
		if filepath.Base(dp.Root) == "good" {
			found = true
		}
		if filepath.Base(dp.Root) == "inner" {
			t.Error("scanned inside a permission-denied directory")
		}
	}
	if !found {
		t.Error("expected the unblocked 'good' project to still be detected")
	}
}

func TestScanNonExistentRoot(t *testing.T) {
	_, err := Scan(context.Background(), "/no/such/path/depctl-test")
	if err == nil {
		t.Fatal("expected an error for a non-existent root")
	}
}

func TestScanNoDuplicateEntries(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "proj", "go.mod"))

	got, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want exactly 1: %+v", len(got), got)
	}
}

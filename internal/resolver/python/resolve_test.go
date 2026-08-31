package python

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestResolvePrefersUVOverPoetryAndRequirements(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "uv.lock", `
[[package]]
name = "fromuv"
version = "1.0.0"
source = { registry = "https://pypi.org/simple" }
`)
	writeFile(t, root, "poetry.lock", `
[[package]]
name = "frompoetry"
version = "1.0.0"
`)
	writeFile(t, root, "requirements.txt", "fromreq==1.0.0\n")

	res, err := New().Resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "fromuv" {
		t.Errorf("Dependencies = %+v, want just fromuv (uv.lock takes priority)", res.Dependencies)
	}
}

func TestResolvePrefersPoetryOverRequirements(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "poetry.lock", `
[[package]]
name = "frompoetry"
version = "1.0.0"
`)
	writeFile(t, root, "requirements.txt", "fromreq==1.0.0\n")

	res, err := New().Resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "frompoetry" {
		t.Errorf("Dependencies = %+v, want just frompoetry", res.Dependencies)
	}
}

func TestResolveUnsupportedPyprojectOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pyproject.toml", "[project]\nname = \"demo\"\n")

	_, err := New().Resolve(context.Background(), root)
	if err == nil {
		t.Fatal("expected error for pyproject.toml-only project, got nil")
	}
	if !strings.Contains(err.Error(), "no supported lock") {
		t.Errorf("unexpected error: %v", err)
	}
}

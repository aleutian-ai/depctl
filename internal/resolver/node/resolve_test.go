package node

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

func TestResolvePrefersNpmOverPnpm(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"demo"}`)
	writeFile(t, root, "package-lock.json", `{
  "lockfileVersion": 3,
  "packages": {
    "": {},
    "node_modules/fromnpm": { "version": "1.0.0" }
  }
}`)
	writeFile(t, root, "pnpm-lock.yaml", "importers:\n  .: {}\npackages:\n  frompnpm@1.0.0: {}\n")

	res, err := New().Resolve(context.Background(), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "fromnpm" {
		t.Errorf("Dependencies = %+v, want just fromnpm (package-lock.json takes priority)", res.Dependencies)
	}
}

func TestResolveYarnNotYetSupported(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"demo"}`)
	writeFile(t, root, "yarn.lock", "# yarn lockfile v1\n")

	_, err := New().Resolve(context.Background(), root)
	if err == nil {
		t.Fatal("expected error for yarn.lock-only project, got nil")
	}
	if !strings.Contains(err.Error(), "yarn.lock") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveNoLockfile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"name":"demo"}`)

	_, err := New().Resolve(context.Background(), root)
	if err == nil {
		t.Fatal("expected error for package.json with no lockfile, got nil")
	}
	if !strings.Contains(err.Error(), "no supported lockfile") {
		t.Errorf("unexpected error: %v", err)
	}
}

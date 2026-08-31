package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDetectPackageJSON(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "package.json")

	ok, err := New().Detect(context.Background(), root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect = false, want true")
	}
}

func TestDetectNoPackageJSON(t *testing.T) {
	ok, err := New().Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true, want false")
	}
}

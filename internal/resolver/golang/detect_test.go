package golang

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetectFindsGoModAtRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/foo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	ok, err := New().Detect(context.Background(), root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect = false, want true")
	}
}

func TestDetectIgnoresNestedGoMod(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module example.com/foo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	ok, err := New().Detect(context.Background(), root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for nested go.mod, want false")
	}
}

func TestDetectEmptyDirectory(t *testing.T) {
	ok, err := New().Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for empty directory, want false")
	}
}

func TestDetectPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks are bypassed")
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	_, err := New().Detect(context.Background(), root)
	if err == nil {
		t.Error("Detect: expected error for unreadable directory, got nil")
	}
}

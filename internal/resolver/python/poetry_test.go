package python

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizePoetryPyPIPackage(t *testing.T) {
	lock := poetryLockFile{Package: []poetryPackage{
		{Name: "requests", Version: "2.31.0"},
	}}
	deps, err := normalizePoetry(lock)
	if err != nil {
		t.Fatalf("normalizePoetry: %v", err)
	}
	if len(deps) != 1 || deps[0].Version != "2.31.0" || deps[0].ResolvedBy != "poetry.lock" {
		t.Errorf("deps = %+v, want plain resolved requests==2.31.0", deps)
	}
}

func TestNormalizePoetryGitSourcePreservesRev(t *testing.T) {
	lock := poetryLockFile{Package: []poetryPackage{
		{
			Name:    "mylib",
			Version: "0.0.0",
			Source: &poetrySource{
				Type:              "git",
				URL:               "https://github.com/example/mylib.git",
				Reference:         "main",
				ResolvedReference: "abcdef1234567890",
			},
		},
	}}
	deps, err := normalizePoetry(lock)
	if err != nil {
		t.Fatalf("normalizePoetry: %v", err)
	}
	want := "poetry.lock:git:https://github.com/example/mylib.git#abcdef1234567890"
	if len(deps) != 1 || deps[0].ResolvedBy != want {
		t.Errorf("ResolvedBy = %q, want %q", deps[0].ResolvedBy, want)
	}
}

func TestNormalizePoetryDirectorySourceMarkedLocal(t *testing.T) {
	lock := poetryLockFile{Package: []poetryPackage{
		{
			Name:    "localpkg",
			Version: "0.1.0",
			Source:  &poetrySource{Type: "directory", URL: "../localpkg"},
		},
	}}
	deps, err := normalizePoetry(lock)
	if err != nil {
		t.Fatalf("normalizePoetry: %v", err)
	}
	if len(deps) != 1 || deps[0].ResolvedBy != "poetry.lock:local:../localpkg" {
		t.Errorf("deps = %+v, want directory source flagged local", deps)
	}
}

func TestResolvePoetryLockEndToEnd(t *testing.T) {
	root := t.TempDir()
	content := `
[[package]]
name = "requests"
version = "2.31.0"

[[package]]
name = "mylib"
version = "0.0.0"

[package.source]
type = "git"
url = "https://github.com/example/mylib.git"
reference = "main"
resolved_reference = "abcdef1234567890"
`
	if err := os.WriteFile(filepath.Join(root, "poetry.lock"), []byte(content), 0o644); err != nil {
		t.Fatalf("write poetry.lock: %v", err)
	}

	res, err := resolvePoetryLock(root)
	if err != nil {
		t.Fatalf("resolvePoetryLock: %v", err)
	}
	if len(res.Dependencies) != 2 {
		t.Fatalf("Dependencies = %+v, want 2", res.Dependencies)
	}
	if res.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}
}

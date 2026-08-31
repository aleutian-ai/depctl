package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadBuiltinOnly(t *testing.T) {
	reg, err := NewLoader("", "").Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg.manifests) != 6 {
		t.Fatalf("got %d manifests, want 6 seed manifests", len(reg.manifests))
	}
	if len(reg.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none for a clean builtin-only load", reg.Warnings)
	}
}

func TestLoadUserOverridesBuiltinWithWarning(t *testing.T) {
	userDir := t.TempDir()
	writeManifest(t, userDir, "grpc-go.yaml", strings.ReplaceAll(validManifest, "authority: 100", "authority: 80"))

	reg, err := NewLoader(userDir, "").Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m, ok := reg.manifests["grpc-go"]
	if !ok {
		t.Fatal("grpc-go manifest missing")
	}
	if m.Sources[0].Authority != 80 {
		t.Errorf("Authority = %d, want 80 (user override should win)", m.Sources[0].Authority)
	}
	if len(reg.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly 1 conflict warning", reg.Warnings)
	}
}

func TestLoadMalformedUserManifestSkippedWithWarning(t *testing.T) {
	userDir := t.TempDir()
	writeManifest(t, userDir, "broken.yaml", "not: [[valid")

	reg, err := NewLoader(userDir, "").Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg.manifests) != 6 {
		t.Errorf("got %d manifests, want 6 (broken one skipped, builtin still loaded)", len(reg.manifests))
	}
	if len(reg.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly 1", reg.Warnings)
	}
}

func TestLoadMissingDirsAreNotErrors(t *testing.T) {
	reg, err := NewLoader("/does/not/exist", "/also/missing").Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg.manifests) != 6 {
		t.Errorf("got %d manifests, want 6 builtin-only", len(reg.manifests))
	}
}

func TestLoadProjectOverridesUser(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()
	writeManifest(t, userDir, "grpc-go.yaml", strings.ReplaceAll(validManifest, "authority: 100", "authority: 80"))
	writeManifest(t, projectDir, "grpc-go.yaml", strings.ReplaceAll(validManifest, "authority: 100", "authority: 60"))

	reg, err := NewLoader(userDir, projectDir).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.manifests["grpc-go"].Sources[0].Authority != 60 {
		t.Errorf("Authority = %d, want 60 (project takes priority over user)", reg.manifests["grpc-go"].Sources[0].Authority)
	}
}

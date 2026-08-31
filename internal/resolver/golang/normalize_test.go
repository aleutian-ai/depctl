package golang

import (
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestNormalizeExcludesMainModule(t *testing.T) {
	deps, err := normalize([]goModule{
		{Path: "example.com/main", Main: true},
		{Path: "example.com/dep", Version: "v1.0.0"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(deps) != 1 || deps[0].Dependency.Name != "example.com/dep" {
		t.Fatalf("deps = %+v, want only example.com/dep", deps)
	}
}

func TestNormalizeLocalReplaceFlagged(t *testing.T) {
	deps, err := normalize([]goModule{
		{
			Path:    "example.com/foo",
			Version: "v0.0.0",
			Replace: &goModule{Path: "../foolocal"},
		},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("deps = %+v, want 1", deps)
	}
	if deps[0].ResolvedBy != "local:../foolocal" {
		t.Errorf("ResolvedBy = %q, want local: prefix", deps[0].ResolvedBy)
	}
	if deps[0].Dependency.Name != "example.com/foo" {
		t.Errorf("Name = %q, want logical path preserved", deps[0].Dependency.Name)
	}
}

func TestNormalizeVersionReplaceUsesReplacedVersion(t *testing.T) {
	deps, err := normalize([]goModule{
		{
			Path:    "example.com/foo",
			Version: "v1.0.0",
			Replace: &goModule{Path: "example.com/bar", Version: "v1.2.3"},
		},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if deps[0].Version != "v1.2.3" {
		t.Errorf("Version = %q, want replaced version v1.2.3", deps[0].Version)
	}
	if deps[0].Dependency.Name != "example.com/foo" {
		t.Errorf("Name = %q, want original logical path preserved", deps[0].Dependency.Name)
	}
	if deps[0].ResolvedBy != "replace:example.com/bar@v1.2.3" {
		t.Errorf("ResolvedBy = %q, want replace provenance", deps[0].ResolvedBy)
	}
}

func TestNormalizeDirectVsIndirect(t *testing.T) {
	deps, err := normalize([]goModule{
		{Path: "example.com/direct", Version: "v1.0.0", Indirect: false},
		{Path: "example.com/indirect", Version: "v1.0.0", Indirect: true},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := map[string]bool{}
	for _, d := range deps {
		got[d.Dependency.Name] = d.Dependency.Direct
	}
	if !got["example.com/direct"] {
		t.Error("example.com/direct: Direct = false, want true")
	}
	if got["example.com/indirect"] {
		t.Error("example.com/indirect: Direct = true, want false")
	}
}

func TestNormalizeEmptyPathIsError(t *testing.T) {
	_, err := normalize([]goModule{{Path: "", Version: "v1.0.0"}})
	if err == nil {
		t.Fatal("expected error for empty Path, got nil")
	}
}

func TestNormalizeEcosystemIsGo(t *testing.T) {
	deps, err := normalize([]goModule{{Path: "example.com/dep", Version: "v1.0.0"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if deps[0].Dependency.Ecosystem != domain.EcosystemGo {
		t.Errorf("Ecosystem = %q, want %q", deps[0].Dependency.Ecosystem, domain.EcosystemGo)
	}
}

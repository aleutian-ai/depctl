package python

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestNormalizeUVDirectVsTransitive(t *testing.T) {
	lock := uvLockFile{Package: []uvPackage{
		{
			Name:    "demo",
			Version: "0.1.0",
			Source:  uvSource{Virtual: "."},
			Dependencies: []uvPkgName{
				{Name: "requests"},
			},
		},
		{Name: "requests", Version: "2.34.2", Source: uvSource{Registry: "https://pypi.org/simple"}},
		{Name: "idna", Version: "3.19", Source: uvSource{Registry: "https://pypi.org/simple"}},
	}}

	deps, err := normalizeUV(lock)
	if err != nil {
		t.Fatalf("normalizeUV: %v", err)
	}

	got := map[string]bool{}
	for _, d := range deps {
		got[d.Dependency.Name] = d.Dependency.Direct
	}
	if len(got) != 2 {
		t.Fatalf("deps = %+v, want exactly requests and idna (virtual root excluded)", deps)
	}
	if !got["requests"] {
		t.Error("requests: Direct = false, want true")
	}
	if got["idna"] {
		t.Error("idna: Direct = true, want false")
	}
}

func TestNormalizeUVGitSource(t *testing.T) {
	lock := uvLockFile{Package: []uvPackage{
		{Name: "flask", Version: "3.2.0.dev0", Source: uvSource{Git: "https://github.com/pallets/flask#d318b683471101618febed18996405ad26462110"}},
	}}

	deps, err := normalizeUV(lock)
	if err != nil {
		t.Fatalf("normalizeUV: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("deps = %+v, want 1", deps)
	}
	want := "uv.lock:git:https://github.com/pallets/flask#d318b683471101618febed18996405ad26462110"
	if deps[0].ResolvedBy != want {
		t.Errorf("ResolvedBy = %q, want %q (rev preserved)", deps[0].ResolvedBy, want)
	}
}

func TestNormalizeUVEditableLocal(t *testing.T) {
	lock := uvLockFile{Package: []uvPackage{
		{Name: "localpkg", Version: "0.1.0", Source: uvSource{Editable: "localpkg"}},
	}}

	deps, err := normalizeUV(lock)
	if err != nil {
		t.Fatalf("normalizeUV: %v", err)
	}
	if len(deps) != 1 || deps[0].ResolvedBy != "uv.lock:local:localpkg" {
		t.Errorf("deps = %+v, want editable flagged local", deps)
	}
}

func TestNormalizeUVEmptyNameIsError(t *testing.T) {
	lock := uvLockFile{Package: []uvPackage{{Name: "", Version: "1.0.0"}}}
	if _, err := normalizeUV(lock); err == nil {
		t.Fatal("expected error for empty package name, got nil")
	}
}

func TestResolveUVLockEndToEnd(t *testing.T) {
	root := t.TempDir()
	content := `
[[package]]
name = "demo"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "requests" },
]

[[package]]
name = "requests"
version = "2.34.2"
source = { registry = "https://pypi.org/simple" }
dependencies = [
    { name = "idna" },
]

[[package]]
name = "idna"
version = "3.19"
source = { registry = "https://pypi.org/simple" }
`
	if err := os.WriteFile(filepath.Join(root, "uv.lock"), []byte(content), 0o644); err != nil {
		t.Fatalf("write uv.lock: %v", err)
	}

	res, err := resolveUVLock(root)
	if err != nil {
		t.Fatalf("resolveUVLock: %v", err)
	}
	if res.Ecosystem != domain.EcosystemPython {
		t.Errorf("Ecosystem = %q, want python", res.Ecosystem)
	}
	if len(res.Dependencies) != 2 {
		t.Fatalf("Dependencies = %+v, want 2 (demo excluded)", res.Dependencies)
	}
	if res.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}

	res2, err := resolveUVLock(root)
	if err != nil {
		t.Fatalf("second resolveUVLock: %v", err)
	}
	if res.Fingerprint != res2.Fingerprint {
		t.Error("fingerprint not stable across identical resolves")
	}
}

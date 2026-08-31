package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitPnpmKeySimple(t *testing.T) {
	name, version, err := splitPnpmKey("left-pad@1.3.0")
	if err != nil {
		t.Fatalf("splitPnpmKey: %v", err)
	}
	if name != "left-pad" || version != "1.3.0" {
		t.Errorf("got (%q, %q), want (left-pad, 1.3.0)", name, version)
	}
}

func TestSplitPnpmKeyScoped(t *testing.T) {
	name, version, err := splitPnpmKey("@types/node@20.11.0")
	if err != nil {
		t.Fatalf("splitPnpmKey: %v", err)
	}
	if name != "@types/node" || version != "20.11.0" {
		t.Errorf("got (%q, %q), want (@types/node, 20.11.0)", name, version)
	}
}

func TestSplitPnpmKeyPeerDepsSuffix(t *testing.T) {
	name, version, err := splitPnpmKey("is-odd@3.0.1(is-number@6.0.0)")
	if err != nil {
		t.Fatalf("splitPnpmKey: %v", err)
	}
	if name != "is-odd" || version != "3.0.1" {
		t.Errorf("got (%q, %q), want (is-odd, 3.0.1) with peer suffix stripped", name, version)
	}
}

func TestSplitPnpmKeyLegacySlashPrefix(t *testing.T) {
	name, version, err := splitPnpmKey("/left-pad@1.3.0")
	if err != nil {
		t.Fatalf("splitPnpmKey: %v", err)
	}
	if name != "left-pad" || version != "1.3.0" {
		t.Errorf("got (%q, %q), want (left-pad, 1.3.0)", name, version)
	}
}

func TestNormalizePnpmDirectVsTransitive(t *testing.T) {
	lock := pnpmLockFile{
		Importers: map[string]pnpmImporter{
			".": {Dependencies: map[string]pnpmDepRef{"is-odd": {Version: "3.0.1"}}},
		},
		Packages: map[string]any{
			"is-odd@3.0.1":    nil,
			"is-number@6.0.0": nil,
		},
	}
	deps, err := normalizePnpm(lock)
	if err != nil {
		t.Fatalf("normalizePnpm: %v", err)
	}
	got := map[string]bool{}
	for _, d := range deps {
		got[d.Dependency.Name] = d.Dependency.Direct
	}
	if !got["is-odd"] {
		t.Error("is-odd: Direct = false, want true")
	}
	if got["is-number"] {
		t.Error("is-number: Direct = true, want false")
	}
}

func TestResolvePnpmLockEndToEnd(t *testing.T) {
	root := t.TempDir()
	content := `
lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      is-odd:
        specifier: 3.0.1
        version: 3.0.1

packages:
  is-odd@3.0.1:
    resolution: {integrity: sha512-fake==}
  is-number@6.0.0:
    resolution: {integrity: sha512-fake==}

snapshots:
  is-odd@3.0.1:
    dependencies:
      is-number: 6.0.0
  is-number@6.0.0: {}
`
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pnpm-lock.yaml: %v", err)
	}

	res, err := resolvePnpmLock(root)
	if err != nil {
		t.Fatalf("resolvePnpmLock: %v", err)
	}
	if len(res.Dependencies) != 2 {
		t.Fatalf("Dependencies = %+v, want 2", res.Dependencies)
	}
	if res.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}
}

func TestResolvePnpmLockWorkspace(t *testing.T) {
	root := t.TempDir()
	content := `
lockfileVersion: '9.0'

importers:
  .: {}
  packages/foo:
    dependencies:
      left-pad:
        specifier: ^1.3.0
        version: 1.3.0

packages:
  left-pad@1.3.0:
    resolution: {integrity: sha512-fake==}
`
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pnpm-lock.yaml: %v", err)
	}

	res, err := resolvePnpmLock(root)
	if err != nil {
		t.Fatalf("resolvePnpmLock: %v", err)
	}
	if len(res.Dependencies) != 1 || !res.Dependencies[0].Dependency.Direct {
		t.Errorf("Dependencies = %+v, want left-pad direct (attributed via the workspace member importer)", res.Dependencies)
	}
}

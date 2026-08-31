package node

import (
	"os"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestNormalizeNpmDirectVsTransitive(t *testing.T) {
	lock := npmLockFile{Packages: map[string]npmLockPkg{
		"": {Dependencies: map[string]string{"is-odd": "^3.0.1"}},
		"node_modules/is-odd": {
			Version:      "3.0.1",
			Dependencies: map[string]string{"is-number": "^6.0.0"},
		},
		"node_modules/is-number": {Version: "6.0.0"},
	}}

	deps, err := normalizeNpm(lock)
	if err != nil {
		t.Fatalf("normalizeNpm: %v", err)
	}
	got := map[string]bool{}
	for _, d := range deps {
		got[d.Dependency.Name] = d.Dependency.Direct
	}
	if len(got) != 2 {
		t.Fatalf("deps = %+v, want exactly is-odd and is-number", deps)
	}
	if !got["is-odd"] {
		t.Error("is-odd: Direct = false, want true")
	}
	if got["is-number"] {
		t.Error("is-number: Direct = true, want false")
	}
}

func TestNormalizeNpmScopedPackage(t *testing.T) {
	lock := npmLockFile{Packages: map[string]npmLockPkg{
		"":                         {},
		"node_modules/@types/node": {Version: "20.11.0"},
	}}
	deps, err := normalizeNpm(lock)
	if err != nil {
		t.Fatalf("normalizeNpm: %v", err)
	}
	if len(deps) != 1 || deps[0].Dependency.Name != "@types/node" || deps[0].Version != "20.11.0" {
		t.Errorf("deps = %+v, want @types/node==20.11.0", deps)
	}
}

func TestNormalizeNpmNestedTransitive(t *testing.T) {
	lock := npmLockFile{Packages: map[string]npmLockPkg{
		"":                                  {},
		"node_modules/foo/node_modules/bar": {Version: "1.0.0"},
	}}
	deps, err := normalizeNpm(lock)
	if err != nil {
		t.Fatalf("normalizeNpm: %v", err)
	}
	if len(deps) != 1 || deps[0].Dependency.Name != "bar" {
		t.Errorf("deps = %+v, want just bar (collapsed nested key)", deps)
	}
}

func TestNormalizeNpmWorkspaceLinkMarkedLocal(t *testing.T) {
	lock := npmLockFile{Packages: map[string]npmLockPkg{
		"":                 {Dependencies: map[string]string{"foo": "*"}},
		"node_modules/foo": {Link: true, Resolved: "packages/foo"},
		"packages/foo":     {Version: "1.0.0"}, // workspace member's own entry, not node_modules/-prefixed
	}}
	deps, err := normalizeNpm(lock)
	if err != nil {
		t.Fatalf("normalizeNpm: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("deps = %+v, want exactly 1 (workspace member's own key excluded)", deps)
	}
	if deps[0].Dependency.Name != "foo" || deps[0].ResolvedBy != "package-lock.json:local:packages/foo" {
		t.Errorf("deps = %+v, want foo flagged local via package-lock.json:local:packages/foo", deps)
	}
}

func TestNormalizeNpmMissingRootIsError(t *testing.T) {
	lock := npmLockFile{Packages: map[string]npmLockPkg{
		"node_modules/foo": {Version: "1.0.0"},
	}}
	if _, err := normalizeNpm(lock); err == nil {
		t.Fatal("expected error for missing root package entry, got nil")
	}
}

func TestResolveNpmLockEndToEnd(t *testing.T) {
	root := t.TempDir()
	content := `{
  "name": "demo",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "demo",
      "version": "1.0.0",
      "dependencies": { "left-pad": "^1.3.0" },
      "devDependencies": { "is-odd": "^3.0.1" }
    },
    "node_modules/is-number": { "version": "6.0.0", "dev": true },
    "node_modules/is-odd": {
      "version": "3.0.1",
      "dev": true,
      "dependencies": { "is-number": "^6.0.0" }
    },
    "node_modules/left-pad": { "version": "1.3.0" }
  }
}`
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}

	res, err := resolveNpmLock(root)
	if err != nil {
		t.Fatalf("resolveNpmLock: %v", err)
	}
	if res.Ecosystem != domain.EcosystemNode {
		t.Errorf("Ecosystem = %q, want node", res.Ecosystem)
	}
	if len(res.Dependencies) != 3 {
		t.Fatalf("Dependencies = %+v, want 3", res.Dependencies)
	}
	if res.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}

	res2, err := resolveNpmLock(root)
	if err != nil {
		t.Fatalf("second resolveNpmLock: %v", err)
	}
	if res.Fingerprint != res2.Fingerprint {
		t.Error("fingerprint not stable across identical resolves")
	}
}

func TestResolveNpmLockUnsupportedVersion(t *testing.T) {
	root := t.TempDir()
	content := `{"lockfileVersion": 1, "packages": {}}`
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}
	_, err := resolveNpmLock(root)
	if err == nil {
		t.Fatal("expected error for lockfileVersion 1, got nil")
	}
}

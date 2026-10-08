package generation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/registry"
	"github.com/aleutian-ai/depctl/internal/source/git"
)

func TestGoModCacheSeedFindsExtractedModule(t *testing.T) {
	cacheDir := t.TempDir()
	// "Widget" (uppercase) proves module.EscapePath's "!lowercase"
	// encoding is actually applied, not just a lucky all-lowercase match.
	modDir := filepath.Join(cacheDir, "example.com", "!widget@v1.2.3")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "widget.go"), []byte("package widget\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("GOMODCACHE", cacheDir)

	dir, ok := goModCacheSeed(context.Background(), "example.com/Widget", "v1.2.3")
	if !ok {
		t.Fatal("goModCacheSeed: ok = false, want true")
	}
	if dir != modDir {
		t.Errorf("dir = %q, want %q", dir, modDir)
	}
}

func TestGoModCacheSeedMissWhenVersionAbsent(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("GOMODCACHE", cacheDir)

	if _, ok := goModCacheSeed(context.Background(), "example.com/widget", "v9.9.9"); ok {
		t.Error("goModCacheSeed: ok = true for a version never extracted, want false")
	}
}

func TestNodeModulesSeedVersionMatch(t *testing.T) {
	projectRoot := t.TempDir()
	pkgDir := filepath.Join(projectRoot, "node_modules", "@types", "node")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"20.11.0"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dir, ok := nodeModulesSeed("@types/node", "20.11.0", projectRoot)
	if !ok {
		t.Fatal("nodeModulesSeed: ok = false, want true")
	}
	if dir != pkgDir {
		t.Errorf("dir = %q, want %q", dir, pkgDir)
	}
}

func TestNodeModulesSeedVersionMismatchFallsBack(t *testing.T) {
	projectRoot := t.TempDir()
	pkgDir := filepath.Join(projectRoot, "node_modules", "lodash")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"4.17.20"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, ok := nodeModulesSeed("lodash", "4.17.21", projectRoot); ok {
		t.Error("nodeModulesSeed: ok = true despite a version mismatch, want false")
	}
}

func TestNodeModulesSeedNoProjectRootNeverHits(t *testing.T) {
	if _, ok := nodeModulesSeed("lodash", "4.17.21", ""); ok {
		t.Error("nodeModulesSeed: ok = true with no projectRoot, want false")
	}
}

func TestFindDistInfoSeedResolvesViaTopLevelTxt(t *testing.T) {
	siteDir := t.TempDir()
	// PyYAML is the canonical real-world case: the distribution name and
	// its actual import directory ("yaml") don't match at all.
	distInfo := filepath.Join(siteDir, "PyYAML-6.0.1.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distInfo, "top_level.txt"), []byte("yaml\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	importDir := filepath.Join(siteDir, "yaml")
	if err := os.MkdirAll(importDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	dir, ok := findDistInfoSeed(siteDir, "PyYAML", "6.0.1")
	if !ok {
		t.Fatal("findDistInfoSeed: ok = false, want true")
	}
	if dir != importDir {
		t.Errorf("dir = %q, want %q", dir, importDir)
	}
}

func TestFindDistInfoSeedNameNormalizationMatchesAcrossSeparators(t *testing.T) {
	siteDir := t.TempDir()
	// Distribution name uses underscores; the resolved dependency name
	// (as PyPI itself would report it) uses dashes — PEP 503 says these
	// are the same package.
	distInfo := filepath.Join(siteDir, "my_cool_package-1.0.0.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distInfo, "top_level.txt"), []byte("my_cool_package\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(siteDir, "my_cool_package"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if _, ok := findDistInfoSeed(siteDir, "my-cool-package", "1.0.0"); !ok {
		t.Error("findDistInfoSeed: ok = false for a name differing only by separator/case, want true")
	}
}

func TestFindDistInfoSeedVersionMismatchFallsBack(t *testing.T) {
	siteDir := t.TempDir()
	distInfo := filepath.Join(siteDir, "requests-2.31.0.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distInfo, "top_level.txt"), []byte("requests\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, ok := findDistInfoSeed(siteDir, "requests", "2.32.0"); ok {
		t.Error("findDistInfoSeed: ok = true despite a version mismatch, want false")
	}
}

func TestFindDistInfoSeedMissingTopLevelTxtNeverGuesses(t *testing.T) {
	siteDir := t.TempDir()
	distInfo := filepath.Join(siteDir, "requests-2.31.0.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// No top_level.txt written — even though a directory named "requests"
	// exists right there and a name-based guess would happen to be
	// correct, findDistInfoSeed must never guess.
	if err := os.MkdirAll(filepath.Join(siteDir, "requests"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if _, ok := findDistInfoSeed(siteDir, "requests", "2.31.0"); ok {
		t.Error("findDistInfoSeed: ok = true with no top_level.txt to confirm the import dir, want false (never guess)")
	}
}

func TestNormalizePyDistName(t *testing.T) {
	cases := map[string]string{
		"PyYAML":          "pyyaml",
		"my_cool_package": "my-cool-package",
		"my-cool-package": "my-cool-package",
		"Django.Rest":     "django-rest",
		"---leading":      "leading",
	}
	for in, want := range cases {
		if got := normalizePyDistName(in); got != want {
			t.Errorf("normalizePyDistName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildUsesLocalGoModCacheAndNeverClonesOverTheNetwork is GIT-004's
// own end-to-end proof: the registry source's URL is deliberately
// unreachable, so if the code ever fell through to EnsureMirror, the
// whole build would fail — it must not, because the local-cache hit
// short-circuits git entirely.
func TestBuildUsesLocalGoModCacheAndNeverClonesOverTheNetwork(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	cacheDir := t.TempDir()
	modDir := filepath.Join(cacheDir, "example.com", "widget@v1.0.0")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, modDir, "README.md", "# Widget\n\nA small example package.\n")
	writeFile(t, modDir, "widget.go", "// Package widget does something.\npackage widget\n\n// Do does something.\nfunc Do() {}\n")
	t.Setenv("GOMODCACHE", cacheDir)

	dep := testDependency()
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sources := []registry.Source{{
		ID: "repository", Type: "git",
		URL: "https://invalid.invalid/nonexistent/widget.git", // never actually reachable
		Ref: "v${version}", Authority: 100,
	}}
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build: %v (a real network clone must never have been attempted)", err)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenIndexing {
		t.Errorf("gen.State = %s, want %s", got.State, domain.GenIndexing)
	}
}

package generation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/source/git"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ragctl-test", "GIT_AUTHOR_EMAIL=ragctl-test@example.com",
		"GIT_COMMITTER_NAME=ragctl-test", "GIT_COMMITTER_EMAIL=ragctl-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newFixtureRepo creates a repo with a Markdown README and a documented
// Go package, commits and tags it v1.0.0, and returns the repo directory.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Widget\n\nA small example package.\n")
	writeFile(t, dir, "widget.go", "// Package widget does something.\npackage widget\n\n// Do does something.\nfunc Do() {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	runGit(t, dir, "tag", "v1.0.0")
	return dir
}

func testStores(t *testing.T) (*bbolt.Store, *badger.Store) {
	t.Helper()
	bs, err := bbolt.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { bs.Close() })

	dds, err := badger.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { dds.Close() })

	return bs, dds
}

func testDependency() domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}
}

func TestCreatePersistsPlannedGeneration(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := testStores(t)

	gen, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gen.State != domain.GenPlanned {
		t.Errorf("gen.State = %s, want %s", gen.State, domain.GenPlanned)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenPlanned {
		t.Errorf("GetGeneration.State = %s, want %s", got.State, domain.GenPlanned)
	}

	data, err := badgerStore.GetManifest(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if len(data) == 0 {
		t.Error("manifest is empty")
	}
}

func TestCreateGeneratesDistinctIDs(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := testStores(t)

	a, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	b, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("two generations for the same dependency version got the same ID: %s", a.ID)
	}
}

func TestBuildEndToEnd(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenIndexing {
		t.Errorf("gen.State = %s, want %s", got.State, domain.GenIndexing)
	}

	manifest, err := getManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		t.Fatalf("getManifest: %v", err)
	}
	if manifest.ObjectCount < 2 {
		t.Errorf("manifest.ObjectCount = %d, want >= 2 (markdown + go doc)", manifest.ObjectCount)
	}
	if manifest.ChunkCount == 0 {
		t.Error("manifest.ChunkCount = 0, want > 0")
	}
	if manifest.ObjectsCreated != manifest.ObjectCount {
		t.Errorf("manifest.ObjectsCreated = %d, want %d (nothing to reuse yet)", manifest.ObjectsCreated, manifest.ObjectCount)
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	if len(chunks) != manifest.ChunkCount {
		t.Errorf("ListGenerationChunks returned %d, manifest says %d", len(chunks), manifest.ChunkCount)
	}
}

func TestBuildAcquisitionFailureMarksGenerationFailed(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No "v1.0.0"-shaped tag exists under this template.
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "does-not-exist-${version}", Authority: 100}}
	err = Build(ctx, gen, sources, gitCache, store, badgerStore)
	if err == nil {
		t.Fatal("Build succeeded, want acquisition failure")
	}
	if !errors.Is(err, ErrAcquisition) {
		t.Errorf("Build error = %v, want wrapping ErrAcquisition", err)
	}

	got, getErr := store.GetGeneration(ctx, gen.ID)
	if getErr != nil {
		t.Fatalf("GetGeneration: %v", getErr)
	}
	if got.State != domain.GenFailed {
		t.Errorf("gen.State = %s, want %s", got.State, domain.GenFailed)
	}
	if got.Error == "" {
		t.Error("gen.Error is empty, want a stored failure message")
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("ListGenerationChunks = %d chunks, want 0 after acquisition failure", len(chunks))
	}
}

func TestBuildContentReuseAcrossGenerations(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}

	gen1, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create gen1: %v", err)
	}
	if err := Build(ctx, gen1, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build gen1: %v", err)
	}
	manifest1, err := getManifest(ctx, badgerStore, gen1.ID)
	if err != nil {
		t.Fatalf("getManifest gen1: %v", err)
	}

	gen2, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create gen2: %v", err)
	}
	if err := Build(ctx, gen2, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build gen2: %v", err)
	}
	manifest2, err := getManifest(ctx, badgerStore, gen2.ID)
	if err != nil {
		t.Fatalf("getManifest gen2: %v", err)
	}

	if manifest2.ObjectsReused != manifest1.ObjectCount {
		t.Errorf("gen2.ObjectsReused = %d, want %d (all of gen1's objects, unchanged content)", manifest2.ObjectsReused, manifest1.ObjectCount)
	}
	if manifest2.ObjectsCreated != 0 {
		t.Errorf("gen2.ObjectsCreated = %d, want 0", manifest2.ObjectsCreated)
	}

	// Now change one file and rebuild at a new version; exactly one new
	// object should be created, the rest reused despite every object's
	// source commit having changed.
	writeFile(t, repoDir, "README.md", "# Widget\n\nAn updated example package.\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "update readme")
	runGit(t, repoDir, "tag", "v1.0.1")

	// Build only mirrors on demand (EnsureMirror is a no-op once cloned);
	// refreshing the mirror's tags is the caller's job, same as a real
	// planner would do before rebuilding.
	repoPath, err := gitCache.EnsureMirror(ctx, repoDir)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if err := gitCache.FetchTags(ctx, repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}

	dep2 := dep
	dep2.Version = "v1.0.1"
	gen3, err := Create(ctx, store, badgerStore, dep2)
	if err != nil {
		t.Fatalf("Create gen3: %v", err)
	}
	if err := Build(ctx, gen3, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build gen3: %v", err)
	}
	manifest3, err := getManifest(ctx, badgerStore, gen3.ID)
	if err != nil {
		t.Fatalf("getManifest gen3: %v", err)
	}
	if manifest3.ObjectsCreated != 1 {
		t.Errorf("gen3.ObjectsCreated = %d, want 1 (only the changed README)", manifest3.ObjectsCreated)
	}
	if manifest3.ObjectsReused != manifest3.ObjectCount-1 {
		t.Errorf("gen3.ObjectsReused = %d, want %d", manifest3.ObjectsReused, manifest3.ObjectCount-1)
	}
}

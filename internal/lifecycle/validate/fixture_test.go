package validate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
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
// Go package, commits and tags it version, and returns the repo
// directory — same shape as internal/data/generation's own fixture, kept
// package-local since Go test helpers aren't exported across packages.
func newFixtureRepo(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Widget\n\nA small example package.\n")
	writeFile(t, dir, "widget.go", "// Package widget does something.\npackage widget\n\n// Do does something.\nfunc Do() {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	runGit(t, dir, "tag", version)
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

// fakeEmbedder is a minimal deterministic embedding.Embedder for tests.
type fakeEmbedder struct{ dims int }

func (f *fakeEmbedder) Name() string    { return "fake" }
func (f *fakeEmbedder) ModelID() string { return "fake-model" }
func (f *fakeEmbedder) Dimensions(ctx context.Context) (int, error) {
	return f.dims, nil
}
func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dims)
		for j := range v {
			v[j] = float32((len(t) + j*7) % 97)
		}
		out[i] = v
	}
	return out, nil
}

// buildAndReplicate runs a full Build+Replicate for dep against repoDir,
// returning the resulting generation and its decoded manifest — the
// real (not simulated) fixture every VAL-* test builds on.
func buildAndReplicate(t *testing.T, ctx context.Context, store *bbolt.Store, badgerStore *badger.Store, gitCache *git.Cache, repoDir string, dep domain.DependencyVersion, embedder *fakeEmbedder, vb backend.VectorBackend, ns backend.Namespace) (domain.Generation, generation.Manifest, domain.BackendReplica) {
	t.Helper()

	gen, err := generation.Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
	if err := generation.Build(ctx, gen, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := generation.Replicate(ctx, gen, sources, embedder, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate: %v", err)
	}

	manifest := readManifest(t, ctx, badgerStore, gen.ID)
	replica, err := store.GetBackendReplica(ctx, gen.ID, vb.Name())
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	return got, manifest, replica
}

func readManifest(t *testing.T, ctx context.Context, badgerStore *badger.Store, generationID string) generation.Manifest {
	t.Helper()
	data, err := badgerStore.GetManifest(ctx, generationID)
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	var m generation.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

func testDependency(version string) domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    version,
	}
}

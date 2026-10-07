package gc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/retention"
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
			v[j] = float32((len(t) + j) % 97)
		}
		out[i] = v
	}
	return out, nil
}

// TestGCEndToEndRemovesOrphanedVersionLeavesReferencedVersionUntouched
// is RET-004's own acceptance criterion, proven against real bbolt,
// Badger, and a fake vector backend: build and replicate two real
// generations of the same dependency (an orphaned one whose grace period
// has expired, and one still actively referenced by a project), run
// PlanGC + Run, and confirm the orphaned version's data is gone from all
// three stores while the referenced version's data survives untouched.
func TestGCEndToEndRemovesOrphanedVersionLeavesReferencedVersionUntouched(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	store, err := bbolt.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()
	badgerStore, err := badger.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	defer badgerStore.Close()
	gitCache := git.NewCache(t.TempDir())
	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	repoDir := newFixtureRepo(t, "v1.0.0")
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}

	// Orphaned version: v1.0.0, built, replicated, then its only
	// reference is dropped and grace already expired.
	orphanedDep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"}
	orphanGen, err := generation.Create(ctx, store, badgerStore, orphanedDep)
	if err != nil {
		t.Fatalf("Create orphan: %v", err)
	}
	if err := generation.Build(ctx, orphanGen, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build orphan: %v", err)
	}
	if err := generation.Replicate(ctx, orphanGen, sources, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate orphan: %v", err)
	}
	if err := store.AddReference(ctx, domain.VersionReference{
		ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "example.com/widget", Version: "v1.0.0", Reason: domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	if err := retention.DropReference(ctx, store, domain.EcosystemGo, "example.com/widget", "v1.0.0", "proj_1"); err != nil {
		t.Fatalf("DropReference: %v", err)
	}
	// Backdate the grace reference so it's already expired.
	if err := store.AddReference(ctx, domain.VersionReference{
		ProjectID: domain.GracePeriodProjectID, Ecosystem: domain.EcosystemGo, Package: "example.com/widget", Version: "v1.0.0",
		Reason: domain.ReferenceReasonGracePeriod, LastSeenAt: time.Now().Add(-1000 * time.Hour),
	}); err != nil {
		t.Fatalf("AddReference (backdated grace): %v", err)
	}

	// Referenced version: v1.0.1, different content (so it's a distinct
	// generation, not reused), still actively referenced by a project.
	writeFile(t, repoDir, "README.md", "# Widget\n\nAn updated description.\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "update")
	runGit(t, repoDir, "tag", "v1.0.1")
	repoPath, err := gitCache.EnsureMirror(ctx, repoDir)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if err := gitCache.FetchTags(ctx, repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}
	referencedDep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.1"}
	refGen, err := generation.Create(ctx, store, badgerStore, referencedDep)
	if err != nil {
		t.Fatalf("Create referenced: %v", err)
	}
	if err := generation.Build(ctx, refGen, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build referenced: %v", err)
	}
	if err := generation.Replicate(ctx, refGen, sources, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate referenced: %v", err)
	}
	if err := store.AddReference(ctx, domain.VersionReference{
		ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "example.com/widget", Version: "v1.0.1", Reason: domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference (referenced): %v", err)
	}

	// Plan and run GC.
	candidates, err := retention.PlanGC(ctx, store, "fake", 0, time.Now())
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Version != "v1.0.0" {
		t.Fatalf("candidates = %+v, want exactly v1.0.0", candidates)
	}

	results, err := Run(ctx, store, badgerStore, vb, ns, candidates)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results = %+v, want one succeeded result", results)
	}

	// Orphaned version's data must be gone from all three stores.
	if _, err := store.GetGeneration(ctx, orphanGen.ID); !errors.Is(err, bbolt.ErrNotFound) {
		t.Errorf("orphaned generation record still exists: err=%v", err)
	}
	chunks, err := badgerStore.ListGenerationChunks(ctx, orphanGen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks (orphan): %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("orphaned generation still has %d chunks in Badger", len(chunks))
	}
	orphanPoints, err := vb.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: make([]float32, 4), TopK: 1000, Filter: &backend.Filter{Version: "v1.0.0"}})
	if err != nil {
		t.Fatalf("Query (orphan): %v", err)
	}
	if len(orphanPoints.Points) != 0 {
		t.Errorf("orphaned version still has %d points in the vector backend", len(orphanPoints.Points))
	}
	remainingRefs, err := store.ListReferences(ctx, domain.EcosystemGo, "example.com/widget", "v1.0.0")
	if err != nil {
		t.Fatalf("ListReferences (orphan): %v", err)
	}
	if len(remainingRefs) != 0 {
		t.Errorf("orphaned version still has %d reference records", len(remainingRefs))
	}

	// Referenced version must be completely untouched.
	if _, err := store.GetGeneration(ctx, refGen.ID); err != nil {
		t.Errorf("referenced generation record was deleted: %v", err)
	}
	refChunks, err := badgerStore.ListGenerationChunks(ctx, refGen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks (referenced): %v", err)
	}
	if len(refChunks) == 0 {
		t.Error("referenced generation's chunks were deleted from Badger")
	}
	refPoints, err := vb.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: make([]float32, 4), TopK: 1000, Filter: &backend.Filter{Version: "v1.0.1"}})
	if err != nil {
		t.Fatalf("Query (referenced): %v", err)
	}
	if len(refPoints.Points) == 0 {
		t.Error("referenced version's points were deleted from the vector backend")
	}

	// Idempotency: re-running against the same (now-empty) candidate
	// list, and re-running Run against the same already-succeeded job,
	// must not error.
	candidatesAfter, err := retention.PlanGC(ctx, store, "fake", 0, time.Now())
	if err != nil {
		t.Fatalf("PlanGC (after): %v", err)
	}
	if len(candidatesAfter) != 0 {
		t.Errorf("candidatesAfter = %+v, want none left", candidatesAfter)
	}
	if _, err := Run(ctx, store, badgerStore, vb, ns, candidates); err != nil {
		t.Fatalf("Run (re-run on already-succeeded job): %v", err)
	}
}

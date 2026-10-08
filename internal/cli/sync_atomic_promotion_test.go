package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/backendtest"
	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	badgerstore "github.com/aleutian-ai/depctl/internal/data/badger"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/embedding"
	"github.com/aleutian-ai/depctl/internal/planner"
	"github.com/aleutian-ai/depctl/internal/query"
	"github.com/aleutian-ai/depctl/internal/registry"
	"github.com/aleutian-ai/depctl/internal/source/git"
)

// VALID-001: proves the atomic-promotion invariant under an injected
// mid-build failure — the single most load-bearing correctness claim
// this project makes (see docs/tickets/backlog/45-competitive-validation)
// — rather than trusting it by design/reasoning alone. Runs against a
// real git fixture repo, real bbolt/Badger stores, and the real
// syncVersion function (not a reimplemented orchestration), with only
// the embedder faked to fail deterministically.

// atomicPromotionFakeEmbedder is a minimal deterministic embedder, same
// failAfter pattern as internal/data/generation's own fakeEmbedder (that
// one is unexported and lives in a different package, so this is a
// small, deliberate duplicate rather than a cross-package reach-in).
// calls is an atomic.Int32, not a plain int — VALID-001's own tests only
// ever used this single-threaded, but epic 53/COORD-003's worker-pool
// tests share one instance across concurrent goroutines.
type atomicPromotionFakeEmbedder struct {
	dims      int
	failAfter int // if > 0, the (failAfter+1)th Embed call fails
	calls     atomic.Int32
}

func (f *atomicPromotionFakeEmbedder) Name() string    { return "fake" }
func (f *atomicPromotionFakeEmbedder) ModelID() string { return "fake-model" }
func (f *atomicPromotionFakeEmbedder) Dimensions(ctx context.Context) (int, error) {
	return f.dims, nil
}
func (f *atomicPromotionFakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	calls := f.calls.Add(1)
	if f.failAfter > 0 && int(calls) >= f.failAfter {
		return nil, errors.New("atomicPromotionFakeEmbedder: simulated failure")
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dims)
		for j := range v {
			v[j] = float32(len(t))
		}
		out[i] = v
	}
	return out, nil
}

func requireGitForAtomicPromotionTest(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// atomicPromotionRunGit runs git in dir with a deterministic author,
// matching internal/data/generation's own test helper (unexported and
// in a different package, so duplicated here rather than reached into).
func atomicPromotionRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=depctl-test", "GIT_AUTHOR_EMAIL=depctl-test@example.com",
		"GIT_COMMITTER_NAME=depctl-test", "GIT_COMMITTER_EMAIL=depctl-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// atomicPromotionFixtureRepo creates a two-tag fixture repo: v1.0.0 and
// v1.1.0, each with distinct, identifiable content — so a test can prove
// which version's content a query actually returns.
func atomicPromotionFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	atomicPromotionRunGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Widget v1\n\nThe original widget behavior.\n")
	atomicPromotionRunGit(t, dir, "add", ".")
	atomicPromotionRunGit(t, dir, "commit", "-q", "-m", "v1.0.0")
	atomicPromotionRunGit(t, dir, "tag", "v1.0.0")

	writeFile(t, dir, "README.md", "# Widget v1.1\n\nThe upgraded widget behavior, materially different text.\n")
	atomicPromotionRunGit(t, dir, "add", ".")
	atomicPromotionRunGit(t, dir, "commit", "-q", "-m", "v1.1.0")
	atomicPromotionRunGit(t, dir, "tag", "v1.1.0")

	return dir
}

const atomicPromotionManifestYAML = `apiVersion: depctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: example.com/widget
match:
  ecosystems: [go]
  packages: [example.com/widget]
version:
  strategy: none
sources:
  - id: repository
    type: git
    url: %[1]s
    ref: v${version}
    authority: 100
`

func atomicPromotionTestRegistry(t *testing.T, repoDir string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	content := fmt.Sprintf(atomicPromotionManifestYAML, repoDir)
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	reg, err := registry.NewLoader(dir, "").Load(context.Background())
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

// TestSyncVersionAtomicPromotionUnderReplicateFailure is VALID-001's
// core scenario: a project resolves example.com/widget v1.0.0, syncs it
// to a real, promoted, active generation, then the project's resolution
// moves to v1.1.0 and that sync's Replicate step fails partway through.
func TestSyncVersionAtomicPromotionUnderReplicateFailure(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()

	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()
	badgerStore, err := badgerstore.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	defer badgerStore.Close()

	repoDir := atomicPromotionFixtureRepo(t)
	reg := atomicPromotionTestRegistry(t, repoDir)
	gitCache := git.NewCache(t.TempDir())
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}

	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}

	// A project genuinely resolving this dependency — needed for
	// query.Service.SearchKnowledge's ModeProject path below, which
	// resolves "what version does this project want" from a real stored
	// Resolution, not from the generation itself.
	const projectID = "proj_valid001"

	// 1. First sync (v1.0.0): must succeed and promote.
	action1 := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: projectID, Dependency: dep}
	embedderOK := &atomicPromotionFakeEmbedder{dims: 4}
	if err := syncVersion(ctx, store, badgerStore, gitCache, &embedding.Prompted{Embedder: embedderOK}, vb, ns, reg, action1, false); err != nil {
		t.Fatalf("first sync (v1.0.0): %v", err)
	}

	activeBefore, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", "v1.0.0", vb.Name())
	if err != nil {
		t.Fatalf("GetActiveGeneration after first sync: %v", err)
	}
	if activeBefore.State != domain.GenActive {
		t.Fatalf("active generation after first sync State = %v, want ACTIVE", activeBefore.State)
	}

	if err := store.PutProject(ctx, domain.Project{ID: projectID, Root: t.TempDir()}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}

	// The project now resolves v1.1.0 — the state a real "dependency
	// upgraded" scan would produce.
	if err := store.PutResolution(ctx, projectID, domain.Resolution{
		Ecosystem:    domain.EcosystemGo,
		Dependencies: []domain.DependencyVersion{{Dependency: dep.Dependency, Version: "v1.1.0"}},
	}); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	// 2. Second sync (v1.1.0): Replicate's embedder fails on its very
	// first call — the fixture's tiny content fits in Replicate's single
	// 64-chunk batch, so this means zero points are ever upserted for
	// the candidate generation (the strongest form of "no partial
	// replica," not merely an unreachable one).
	dep11 := domain.DependencyVersion{Dependency: dep.Dependency, Version: "v1.1.0"}
	action2 := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: projectID, Dependency: dep11}
	embedderFail := &atomicPromotionFakeEmbedder{dims: 4, failAfter: 1}
	err = syncVersion(ctx, store, badgerStore, gitCache, &embedding.Prompted{Embedder: embedderFail}, vb, ns, reg, action2, false)
	if err == nil {
		t.Fatal("second sync (v1.1.0, injected Replicate failure) succeeded, want an error")
	}
	t.Logf("second sync failed as expected: %v", err)

	// 3. The prior active generation must be untouched — promote.Promote
	// must never have been called for the failed candidate.
	activeAfter, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", "v1.0.0", vb.Name())
	if err != nil {
		t.Fatalf("GetActiveGeneration after failed second sync: %v", err)
	}
	if activeAfter.ID != activeBefore.ID {
		t.Fatalf("active generation changed after a failed sync: before=%s after=%s", activeBefore.ID, activeAfter.ID)
	}

	// 4. The candidate generation must end up FAILED, not stuck at
	// whatever state Build left it in. Originally found NOT to hold
	// (Replicate's own failure path only marked the BackendReplica
	// FAILED, leaving Generation.State at INDEXING) — fixed by making
	// failReplica also call generation.fail, same as Build's own failure
	// path already did. See internal/data/generation/replicate.go's
	// failReplica and TestReplicateFailureMarksReplicaFailedWithError.
	candidates, err := store.ListGenerationsByDependencyVersion(ctx, domain.EcosystemGo, "example.com/widget", "v1.1.0")
	if err != nil {
		t.Fatalf("ListGenerationsByDependencyVersion: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates for v1.1.0 = %d, want exactly 1 (the failed sync's generation)", len(candidates))
	}
	candidate := candidates[0]
	if candidate.State != domain.GenFailed {
		t.Errorf("candidate generation State = %v after an injected Replicate failure, want FAILED", candidate.State)
	}

	replica, replicaErr := store.GetBackendReplica(ctx, candidate.ID, vb.Name())
	if replicaErr != nil {
		t.Logf("finding: no BackendReplica record exists for the failed candidate generation (%v)", replicaErr)
	} else {
		t.Logf("finding: BackendReplica.Status for the failed candidate generation = %q", replica.Status)
		if replica.Status == "ready" {
			t.Errorf("BackendReplica.Status = ready after an injected embedding failure, want failed")
		}
	}

	// 5. The actual, most important behavior: a query scoped to the
	// project (which now resolves v1.1.0, never promoted) must report a
	// clear ErrNoActiveGeneration — not v1.0.0's content mislabeled as
	// v1.1.0, not partial v1.1.0 content, and (since the fix below) not
	// a silently empty success either. Originally found to succeed with
	// an empty result instead of erroring (searchProject's
	// GetActiveGeneration check only verified *a* generation was active
	// for this ecosystem/name, not that it matched the project's
	// resolved version) — fixed in internal/query/search.go's
	// searchProject.
	svc := query.New(store, badgerStore, vb, &embedding.Prompted{Embedder: embedderOK}, ns, vb.Name())

	_, searchErr := svc.SearchKnowledge(ctx, query.Query{ProjectID: projectID, Dependency: "example.com/widget", Text: "widget", Mode: query.ModeProject})
	if !errors.Is(searchErr, query.ErrNoActiveGeneration) {
		t.Errorf("SearchKnowledge for a project resolving v1.1.0 (never promoted) = %v, want ErrNoActiveGeneration", searchErr)
	}
}

// TestSyncVersionAtomicPromotionUnderBuildFailure is VALID-001's second
// required variant: the injected failure lands during Build (a git ref
// that doesn't exist — an ACQUIRING-stage failure; a real NORMALIZING-
// stage failure would need content specifically crafted to break a
// normalizer, more fixture complexity for no different an invariant) —
// confirming the same "prior generation stays active" invariant holds
// regardless of pipeline stage, and that a Build-stage failure produces
// GenFailed the same way a Replicate-stage failure now does too (see
// TestSyncVersionAtomicPromotionUnderReplicateFailure).
func TestSyncVersionAtomicPromotionUnderBuildFailure(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()

	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	defer store.Close()
	badgerStore, err := badgerstore.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	defer badgerStore.Close()

	repoDir := atomicPromotionFixtureRepo(t)
	reg := atomicPromotionTestRegistry(t, repoDir)
	gitCache := git.NewCache(t.TempDir())
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}
	const projectID = "proj_valid001_build"

	action1 := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: projectID, Dependency: dep}
	embedderOK := &atomicPromotionFakeEmbedder{dims: 4}
	if err := syncVersion(ctx, store, badgerStore, gitCache, &embedding.Prompted{Embedder: embedderOK}, vb, ns, reg, action1, false); err != nil {
		t.Fatalf("first sync (v1.0.0): %v", err)
	}
	activeBefore, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", "v1.0.0", vb.Name())
	if err != nil {
		t.Fatalf("GetActiveGeneration after first sync: %v", err)
	}

	// "v9.9.9" has no matching git tag in the fixture repo — Build's
	// ResolveRef fails, an ACQUIRING-stage failure.
	depBad := domain.DependencyVersion{Dependency: dep.Dependency, Version: "v9.9.9"}
	action2 := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: projectID, Dependency: depBad}
	if err := syncVersion(ctx, store, badgerStore, gitCache, &embedding.Prompted{Embedder: embedderOK}, vb, ns, reg, action2, false); err == nil {
		t.Fatal("second sync (v9.9.9, nonexistent ref) succeeded, want an error")
	}

	activeAfter, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", "v1.0.0", vb.Name())
	if err != nil {
		t.Fatalf("GetActiveGeneration after failed second sync: %v", err)
	}
	if activeAfter.ID != activeBefore.ID {
		t.Fatalf("active generation changed after a failed Build-stage sync: before=%s after=%s", activeBefore.ID, activeAfter.ID)
	}

	candidates, err := store.ListGenerationsByDependencyVersion(ctx, domain.EcosystemGo, "example.com/widget", "v9.9.9")
	if err != nil {
		t.Fatalf("ListGenerationsByDependencyVersion: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates for v9.9.9 = %d, want exactly 1", len(candidates))
	}
	if candidates[0].State != domain.GenFailed {
		t.Errorf("candidate generation State after a Build-stage failure = %v, want FAILED", candidates[0].State)
	}
}

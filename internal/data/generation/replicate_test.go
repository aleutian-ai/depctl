package generation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/backend/keyword"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/source/git"
)

// fakeEmbedder is a minimal deterministic embedding.Embedder for tests —
// each vector is just the text's length repeated, which is enough to
// exercise Replicate's plumbing without needing a real model.
type fakeEmbedder struct {
	dims      int
	failAfter int // if > 0, the (failAfter+1)th Embed call fails
	calls     int
}

func (f *fakeEmbedder) Name() string    { return "fake" }
func (f *fakeEmbedder) ModelID() string { return "fake-model" }
func (f *fakeEmbedder) Dimensions(ctx context.Context) (int, error) {
	return f.dims, nil
}
func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.failAfter > 0 && f.calls >= f.failAfter {
		return nil, errors.New("fake embedder: simulated failure")
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

func TestReplicateEmbedsAndUpsertsAllChunks(t *testing.T) {
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
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build: %v", err)
	}

	manifest, err := getManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		t.Fatalf("getManifest: %v", err)
	}

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4, Distance: "cosine"}

	if err := Replicate(ctx, gen, sources, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate: %v", err)
	}

	replica, err := store.GetBackendReplica(ctx, gen.ID, vb.Name())
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}
	if replica.Status != "complete" {
		t.Errorf("replica.Status = %s, want complete", replica.Status)
	}
	if replica.PointCount != manifest.ChunkCount {
		t.Errorf("replica.PointCount = %d, want %d (manifest chunk count)", replica.PointCount, manifest.ChunkCount)
	}
	if replica.EmbeddingModel != "fake-model" {
		t.Errorf("replica.EmbeddingModel = %s, want fake-model", replica.EmbeddingModel)
	}

	result, err := vb.Query(ctx, backend.QueryRequest{
		Namespace: ns.Name,
		Vector:    make([]float32, 4),
		TopK:      1000,
		Filter:    &backend.Filter{Generation: gen.ID},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Points) != manifest.ChunkCount {
		t.Errorf("backend has %d points for this generation, want %d", len(result.Points), manifest.ChunkCount)
	}
	for _, p := range result.Points {
		if p.Metadata.Ecosystem != "go" || p.Metadata.Dependency != "example.com/widget" || p.Metadata.Version != "v1.0.0" {
			t.Errorf("point metadata = %+v, want ecosystem=go dependency=example.com/widget version=v1.0.0", p.Metadata)
		}
		if p.Metadata.SourceType != "git" || p.Metadata.Authority != 100 {
			t.Errorf("point metadata provenance = %+v, want source_type=git authority=100", p.Metadata)
		}
	}
}

func TestReplicateFailureMarksReplicaFailedWithError(t *testing.T) {
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
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build: %v", err)
	}

	embedder := &fakeEmbedder{dims: 4, failAfter: 1} // fails on the very first Embed call
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	err = Replicate(ctx, gen, sources, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore)
	if err == nil {
		t.Fatal("Replicate succeeded, want error")
	}
	if !errors.Is(err, ErrReplication) {
		t.Errorf("Replicate error = %v, want wrapping ErrReplication", err)
	}

	replica, getErr := store.GetBackendReplica(ctx, gen.ID, vb.Name())
	if getErr != nil {
		t.Fatalf("GetBackendReplica: %v", getErr)
	}
	if replica.Status != "failed" {
		t.Errorf("replica.Status = %s, want failed", replica.Status)
	}
	if replica.LastError == "" {
		t.Error("replica.LastError is empty, want a stored failure message")
	}

	// The Generation record itself must also transition to FAILED, not
	// stay stuck at whatever state Build left it in (INDEXING) — found
	// missing via VALID-001's live testing (docs/tickets/completed/
	// 45-competitive-validation), where an INDEXING-stuck generation was
	// functionally harmless for query correctness but invisible to
	// anything inspecting Generation.State directly (status/doctor,
	// orphan GC's own candidate discovery).
	failedGen, getGenErr := store.GetGeneration(ctx, gen.ID)
	if getGenErr != nil {
		t.Fatalf("GetGeneration: %v", getGenErr)
	}
	if failedGen.State != domain.GenFailed {
		t.Errorf("generation.State = %s, want FAILED", failedGen.State)
	}
	if failedGen.Error == "" {
		t.Error("generation.Error is empty, want the same failure message the replica carries")
	}
}

func TestReplicateWithNoChunksCompletesWithZeroPoints(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := testStores(t)

	dep := testDependency()
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	if err := Replicate(ctx, gen, nil, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate: %v", err)
	}

	replica, err := store.GetBackendReplica(ctx, gen.ID, vb.Name())
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}
	if replica.Status != "complete" || replica.PointCount != 0 {
		t.Errorf("replica = %+v, want complete with 0 points", replica)
	}
}

// TestReplicatePointsUseGenerationVersionNotStaleObjectVersion is a
// regression test: GEN-003 lets a second generation reuse an unchanged
// object created by a prior generation at a different version. A
// reused object's own obj.Dependency.Version still reflects whichever
// generation first created it, so Replicate must stamp every point with
// the generation actually being replicated (gen.Dependency.Version), not
// the object's own stale field — otherwise a reused chunk's vector would
// carry the wrong version, and VAL-003's version-filtered query
// correctness check would be meaningless.
func TestReplicatePointsUseGenerationVersionNotStaleObjectVersion(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep1 := testDependency() // v1.0.0
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}

	gen1, err := Create(ctx, store, badgerStore, dep1)
	if err != nil {
		t.Fatalf("Create gen1: %v", err)
	}
	if err := Build(ctx, gen1, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build gen1: %v", err)
	}

	// Change only widget.go; README.md's object is reused unchanged from
	// gen1, but must still get gen2's version when gen2 is replicated.
	writeFile(t, repoDir, "widget.go", "// Package widget does something else now.\npackage widget\n\n// Do does something.\nfunc Do() {}\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "update widget.go")
	runGit(t, repoDir, "tag", "v1.0.1")
	repoPath, err := gitCache.EnsureMirror(ctx, repoDir)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if err := gitCache.FetchTags(ctx, repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}

	dep2 := dep1
	dep2.Version = "v1.0.1"
	gen2, err := Create(ctx, store, badgerStore, dep2)
	if err != nil {
		t.Fatalf("Create gen2: %v", err)
	}
	if err := Build(ctx, gen2, sources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build gen2: %v", err)
	}
	manifest2, err := getManifest(ctx, badgerStore, gen2.ID)
	if err != nil {
		t.Fatalf("getManifest gen2: %v", err)
	}
	if manifest2.ObjectsReused == 0 {
		t.Fatal("gen2 reused no objects from gen1 — test setup didn't exercise the reuse path")
	}

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	if err := Replicate(ctx, gen2, sources, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate gen2: %v", err)
	}

	result, err := vb.Query(ctx, backend.QueryRequest{
		Namespace: ns.Name,
		Vector:    make([]float32, 4),
		TopK:      1000,
		Filter:    &backend.Filter{Generation: gen2.ID},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Points) == 0 {
		t.Fatal("no points found for gen2")
	}
	for _, p := range result.Points {
		if p.Metadata.Version != "v1.0.1" {
			t.Errorf("point %s has version %s, want v1.0.1 (gen2's version, even for a reused object)", p.ID, p.Metadata.Version)
		}
	}
}

// TestReplicatePointsUseCurrentSourceAuthorityNotStaleObjectAuthority is
// the same regression class as the Version test above, for
// Authority/SourceType: a reused object's own obj.Authority/SourceType
// reflect whichever generation first created it. Authority is
// user/registry-configurable (a source's authority can change between
// syncs — see internal/registry's Loader user-override support), so
// Replicate must stamp every point from the *current* sources list, not
// from the object's stale stored fields.
func TestReplicatePointsUseCurrentSourceAuthorityNotStaleObjectAuthority(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	originalSources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}

	gen1, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create gen1: %v", err)
	}
	if err := Build(ctx, gen1, originalSources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build gen1: %v", err)
	}

	// Re-sync the exact same dependency version — every object is
	// reused unchanged from gen1 — but with the registry source's
	// authority bumped in config, simulating a user override applied
	// between syncs.
	updatedSources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 42}}
	gen2, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create gen2: %v", err)
	}
	if err := Build(ctx, gen2, updatedSources, gitCache, store, badgerStore, ""); err != nil {
		t.Fatalf("Build gen2: %v", err)
	}
	manifest2, err := getManifest(ctx, badgerStore, gen2.ID)
	if err != nil {
		t.Fatalf("getManifest gen2: %v", err)
	}
	if manifest2.ObjectsReused == 0 {
		t.Fatal("gen2 reused no objects from gen1 — test setup didn't exercise the reuse path")
	}

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	if err := Replicate(ctx, gen2, updatedSources, &embedding.Prompted{Embedder: embedder}, vb, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate gen2: %v", err)
	}

	result, err := vb.Query(ctx, backend.QueryRequest{
		Namespace: ns.Name,
		Vector:    make([]float32, 4),
		TopK:      1000,
		Filter:    &backend.Filter{Generation: gen2.ID},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Points) == 0 {
		t.Fatal("no points found for gen2")
	}
	for _, p := range result.Points {
		if p.Metadata.Authority != 42 {
			t.Errorf("point %s has authority %d, want 42 (current source config, even for a reused object)", p.ID, p.Metadata.Authority)
		}
		if p.Metadata.SourceType != "git" {
			t.Errorf("point %s has source_type %q, want git", p.ID, p.Metadata.SourceType)
		}
	}
}

// TestReplicateWithoutEmbedderBuildsKeywordOnly is LOCAL-002's keyword
// path: no embedder, so points carry text and no vector, the replica
// records no embedding model, and a keyword index can search them.
func TestReplicateWithoutEmbedderBuildsKeywordOnly(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	repoDir := newFixtureRepo(t)
	gen, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
	if err := Build(ctx, gen, sources, git.NewCache(t.TempDir()), store, badgerStore, ""); err != nil {
		t.Fatalf("Build: %v", err)
	}
	kw := keyword.New(filepath.Join(t.TempDir(), "keyword.db"))
	ns := backend.Namespace{Name: "ragctl"}

	if err := Replicate(ctx, gen, sources, nil, kw, ns, store, badgerStore); err != nil {
		t.Fatalf("Replicate without an embedder: %v", err)
	}
	replica, err := store.GetBackendReplica(ctx, gen.ID, kw.Name())
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}
	if replica.Status != "complete" || replica.EmbeddingModel != "" {
		t.Errorf("replica = %+v, want complete with no embedding model", replica)
	}
	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("ListGenerationChunks = %d chunks, %v", len(chunks), err)
	}
	res, err := kw.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Text: string(chunks[0].Content), TopK: 3, Filter: &backend.Filter{Generation: gen.ID}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Points) == 0 {
		t.Error("keyword query for a chunk's own text found nothing")
	}
}

// partialUpsert writes the batch, then reports failure: a vector store
// that failed partway through.
type partialUpsert struct{ backend.VectorBackend }

func (p partialUpsert) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	if err := p.VectorBackend.Upsert(ctx, req); err != nil {
		return err
	}
	return errors.New("simulated failure after a partial write")
}

// TestAddToIndexFailureLeavesNothingAndNeverTouchesTheGeneration: the
// generation being backfilled is typically ACTIVE and serving, so a
// failure must neither change its state (as Replicate's failure path
// does) nor leave a partial set of entries behind.
func TestAddToIndexFailureLeavesNothingAndNeverTouchesTheGeneration(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	repoDir := newFixtureRepo(t)
	gen, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
	if err := Build(ctx, gen, sources, git.NewCache(t.TempDir()), store, badgerStore, ""); err != nil {
		t.Fatalf("Build: %v", err)
	}
	before, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	inner := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4, Distance: "cosine"}

	err = AddToIndex(ctx, before, sources, &embedding.Prompted{Embedder: &fakeEmbedder{dims: 4}}, partialUpsert{inner}, ns, badgerStore)
	if err == nil {
		t.Fatal("AddToIndex succeeded, want the simulated failure")
	}
	if n, err := inner.Count(ctx, ns.Name, &backend.Filter{Generation: gen.ID}); err != nil || n != 0 {
		t.Errorf("points left after the failed AddToIndex = %d (err %v), want 0", n, err)
	}
	after, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if after.State != before.State || after.Error != "" {
		t.Errorf("generation after a failed AddToIndex = %s (%q), want unchanged %s", after.State, after.Error, before.State)
	}
}

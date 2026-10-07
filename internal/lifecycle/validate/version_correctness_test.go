package validate

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/source/git"
)

func TestVersionCorrectnessIsolatesTwoIndexedVersions(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t, "v1.0.0")
	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	genV1, _, _ := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)

	// Second version of the same dependency, same content — proves
	// isolation holds even when GEN-003 reuses the underlying objects.
	writeFile(t, repoDir, "README.md", "# Widget\n\nAn updated description.\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "update readme")
	runGit(t, repoDir, "tag", "v1.0.1")
	repoPath, err := gitCache.EnsureMirror(ctx, repoDir)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if err := gitCache.FetchTags(ctx, repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}
	genV2, _, _ := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.1"), embedder, vb, ns)

	chunksV1, err := badgerStore.ListGenerationChunks(ctx, genV1.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks v1: %v", err)
	}
	chunksV2, err := badgerStore.ListGenerationChunks(ctx, genV2.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks v2: %v", err)
	}

	resultV1, err := VersionCorrectness(ctx, &embedding.Prompted{Embedder: embedder}, vb, ns, genV1, SampleChunks(chunksV1))
	if err != nil {
		t.Fatalf("VersionCorrectness v1: %v", err)
	}
	if !resultV1.Passed {
		t.Errorf("VersionCorrectness v1 failed: %v", resultV1.Failures)
	}

	resultV2, err := VersionCorrectness(ctx, &embedding.Prompted{Embedder: embedder}, vb, ns, genV2, SampleChunks(chunksV2))
	if err != nil {
		t.Fatalf("VersionCorrectness v2: %v", err)
	}
	if !resultV2.Passed {
		t.Errorf("VersionCorrectness v2 failed: %v", resultV2.Failures)
	}
}

func TestVersionCorrectnessDetectsCrossVersionContamination(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t, "v1.0.0")
	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	gen, _, _ := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	sample := SampleChunks(chunks)
	if len(sample) == 0 {
		t.Fatal("no chunks to sample")
	}

	// Manually corrupt the fixture: insert a point with the same vector
	// as the first sample chunk (so it would be a near-neighbor of the
	// query) but tagged with a different version — simulating a
	// contaminated replica.
	vecs, err := embedder.Embed(ctx, []string{string(sample[0].Content)})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	err = vb.Upsert(ctx, backend.UpsertRequest{
		Namespace: ns.Name,
		Points: []backend.Point{
			{
				ID:     "chk_corrupt",
				Vector: vecs[0],
				Metadata: backend.PointMetadata{
					Ecosystem: "go", Dependency: "example.com/widget", Version: "v0.9.9-corrupt", Generation: gen.ID,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert corrupt point: %v", err)
	}

	result, err := VersionCorrectness(ctx, &embedding.Prompted{Embedder: embedder}, vb, ns, gen, sample)
	if err != nil {
		t.Fatalf("VersionCorrectness: %v", err)
	}
	if result.Passed {
		t.Fatal("VersionCorrectness.Passed = true, want false (corrupted point should be detected)")
	}
	if len(result.Failures) == 0 {
		t.Error("VersionCorrectness.Failures is empty, want the corrupt point reported")
	}
}

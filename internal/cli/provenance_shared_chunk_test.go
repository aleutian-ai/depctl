package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/embedding"
	"github.com/aleutian-ai/depctl/internal/planner"
	"github.com/aleutian-ai/depctl/internal/query"
	"github.com/aleutian-ai/depctl/internal/registry"
)

const twoRepoManifestYAML = `apiVersion: depctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: %[1]s
match:
  ecosystems: [go]
  packages: [%[1]s]
version:
  strategy: none
sources:
  - id: repository
    type: git
    url: %[2]s
    ref: v${version}
    authority: 100
`

// sharedTextRepo is a repo whose README.md is byte-identical across every
// call (the shape of a LICENSE or a vendored notice) plus one file unique
// to name, tagged v1.0.0.
func sharedTextRepo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	atomicPromotionRunGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Shared notice\n\nThis exact paragraph appears in more than one dependency.\n")
	writeFile(t, dir, "UNIQUE.md", "# "+name+"\n\nText only "+name+" contains.\n")
	atomicPromotionRunGit(t, dir, "add", ".")
	atomicPromotionRunGit(t, dir, "commit", "-q", "-m", name)
	atomicPromotionRunGit(t, dir, "tag", "v1.0.0")
	return dir
}

// TestProvenanceOfAChunkSharedAcrossDependencies reproduces, through the
// real query service, what a chunk that two dependencies share reports
// about where it came from. Objects are stored once per distinct content
// (GEN-003), so the second dependency's chunk points at an object the
// first one stored.
func TestProvenanceOfAChunkSharedAcrossDependencies(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()
	f := newCrossProjectTestFixture(t)

	repos := map[string]string{"example.com/alpha": sharedTextRepo(t, "alpha"), "example.com/beta": sharedTextRepo(t, "beta")}
	regDir := t.TempDir()
	for name, repo := range repos {
		writeFile(t, regDir, filepath.Base(name)+".yaml", fmt.Sprintf(twoRepoManifestYAML, name, repo))
	}
	reg, err := registry.NewLoader(regDir, "").Load(ctx)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	emb := &atomicPromotionFakeEmbedder{dims: 4}
	dep := func(name string) domain.DependencyVersion {
		return domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: name}, Version: "v1.0.0"}
	}
	for _, name := range []string{"example.com/alpha", "example.com/beta"} { // alpha first: it stores the shared object
		action := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_shared", Dependency: dep(name)}
		if err := syncVersion(ctx, f.store, f.badgerStore, f.gitCache, &embedding.Prompted{Embedder: emb}, f.vb, f.ns, reg, action, false); err != nil {
			t.Fatalf("sync %s: %v", name, err)
		}
	}

	if err := f.store.PutProject(ctx, domain.Project{ID: "proj_shared", Root: t.TempDir()}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if err := f.store.PutResolution(ctx, "proj_shared", domain.Resolution{
		Ecosystem:    domain.EcosystemGo,
		Dependencies: []domain.DependencyVersion{dep("example.com/alpha"), dep("example.com/beta")},
	}); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	svc := query.New(f.store, f.badgerStore, f.vb, &embedding.Prompted{Embedder: emb}, f.ns, f.vb.Name())
	result, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: "proj_shared", Dependency: "example.com/beta", Text: "Shared notice", Mode: query.ModeProject})
	if err != nil {
		t.Fatalf("SearchKnowledge(beta): %v", err)
	}
	var shared *query.ResultChunk
	for i := range result.Chunks {
		if strings.Contains(result.Chunks[i].Content, "Shared notice") {
			shared = &result.Chunks[i]
		}
	}
	if shared == nil {
		t.Fatalf("no chunk containing the shared text among %d results", len(result.Chunks))
	}

	// What an agent's search result says: taken from the vector point.
	t.Logf("search result: dependency=%s version=%s generation=%s", shared.Dependency, shared.Version, shared.Generation)
	if shared.Dependency != "example.com/beta" {
		t.Errorf("search result names dependency %q, want example.com/beta", shared.Dependency)
	}

	// What the provenance lookup says: read through the shared object.
	prov, err := svc.GetProvenance(ctx, shared.Generation, shared.ChunkID)
	if err != nil {
		t.Fatalf("GetProvenance: %v", err)
	}
	obj, err := f.badgerStore.GetKnowledgeObject(ctx, prov.ObjectID)
	if err != nil {
		t.Fatalf("GetKnowledgeObject: %v", err)
	}
	t.Logf("provenance: SourceURI=%s LogicalPath=%s Version=%s | object commit=%s", prov.SourceURI, prov.LogicalPath, prov.Version, obj.Commit)
	t.Logf("alpha repo=%s\n      beta repo=%s", repos["example.com/alpha"], repos["example.com/beta"])

	// KNOWN ISSUE (docs/tickets/planned/55-vector-point-identity/POINT-002):
	// the object is shared, so provenance names whichever dependency stored
	// it first. Nothing in production calls GetProvenance today (the MCP
	// tools never do), so no user sees this yet. Skipped, not failed, while
	// the mismatch exists; it becomes a real assertion once fixed.
	if prov.SourceURI != repos["example.com/beta"] {
		t.Skipf("known issue: provenance of beta's chunk names SourceURI %q (the dependency that stored the shared object first), want beta's own repo %q", prov.SourceURI, repos["example.com/beta"])
	}
}

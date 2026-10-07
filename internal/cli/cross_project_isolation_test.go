package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/query"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/retention"
	"aleutian-ai/ragctl/internal/source/git"
)

// VALID-002: a reusable two-project, two-version fixture (the Grounded
// Docs comparison's "Project A vs Project B" scenario) proving
// ModeProject structurally excludes the other project's version-
// mismatched content — not just usually ranks it lower — while
// ModeCompare/ModeAllRetained are confirmed to behave exactly as
// documented (deliberately NOT version-isolated, by design) rather than
// assumed to be.

// crossProjectIsolationFixture builds one dependency module with two
// tagged versions carrying distinct, identifiable content, two real
// synced generations (one per version), and two projects each resolving
// one version — reusable by any test needing this exact shape (VALID-001
// already has its own narrower one-project variant; this is the two-
// project generalization the ticket asks for).
type crossProjectIsolationFixture struct {
	store       *bboltstore.Store
	badgerStore *badgerstore.Store
	vb          *backendtest.Backend
	ns          backend.Namespace
	embedder    *atomicPromotionFakeEmbedder
	projectA    string
	projectB    string
	dep         domain.Dependency
}

func newCrossProjectIsolationFixture(t *testing.T) *crossProjectIsolationFixture {
	t.Helper()
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()

	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	badgerStore, err := badgerstore.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { badgerStore.Close() })

	base := t.TempDir()
	depRoot := filepath.Join(base, "shared-dep")
	writeFile(t, depRoot, "README.md", "# Widget v1\n\nOnly version one mentions the phrase gopher-burrow-marker.\n")
	atomicPromotionRunGit(t, depRoot, "init", "-q", "-b", "main")
	atomicPromotionRunGit(t, depRoot, "add", ".")
	atomicPromotionRunGit(t, depRoot, "commit", "-q", "-m", "v1.0.0")
	atomicPromotionRunGit(t, depRoot, "tag", "v1.0.0")

	writeFile(t, depRoot, "README.md", "# Widget v2\n\nOnly version two mentions the phrase falcon-cascade-token.\n")
	atomicPromotionRunGit(t, depRoot, "add", ".")
	atomicPromotionRunGit(t, depRoot, "commit", "-q", "-m", "v2.0.0")
	atomicPromotionRunGit(t, depRoot, "tag", "v2.0.0")

	regDir := t.TempDir()
	manifest := fmt.Sprintf(atomicPromotionManifestYAML, depRoot)
	writeFile(t, regDir, "manifest.yaml", manifest)
	reg, err := registry.NewLoader(regDir, "").Load(ctx)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	gitCache := git.NewCache(t.TempDir())
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	embedder := &atomicPromotionFakeEmbedder{dims: 4}
	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}

	const projectA = "proj_a"
	const projectB = "proj_b"

	for _, tc := range []struct {
		projectID string
		version   string
	}{{projectA, "v1.0.0"}, {projectB, "v2.0.0"}} {
		depVersion := domain.DependencyVersion{Dependency: dep, Version: tc.version}
		action := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: tc.projectID, Dependency: depVersion}
		if err := syncVersion(ctx, store, badgerStore, gitCache, &embedding.Prompted{Embedder: embedder}, vb, ns, reg, action, false); err != nil {
			t.Fatalf("sync %s for %s: %v", tc.version, tc.projectID, err)
		}
		if err := store.PutProject(ctx, domain.Project{ID: tc.projectID, Root: t.TempDir()}); err != nil {
			t.Fatalf("PutProject %s: %v", tc.projectID, err)
		}
		if err := store.PutResolution(ctx, tc.projectID, domain.Resolution{
			Ecosystem: domain.EcosystemGo, Dependencies: []domain.DependencyVersion{depVersion},
		}); err != nil {
			t.Fatalf("PutResolution %s: %v", tc.projectID, err)
		}
		if err := store.AddReference(ctx, domain.VersionReference{
			ProjectID: tc.projectID, Ecosystem: dep.Ecosystem, Package: dep.Name, Version: tc.version,
			Reason: domain.ReferenceReasonProject,
		}); err != nil {
			t.Fatalf("AddReference %s: %v", tc.projectID, err)
		}
	}
	// v2.0.0 is also "latest" — needed to exercise ModeCompare/ModeLatest.
	if err := store.AddReference(ctx, domain.VersionReference{
		Ecosystem: dep.Ecosystem, Package: dep.Name, Version: "v2.0.0", Reason: domain.ReferenceReasonLatest,
	}); err != nil {
		t.Fatalf("AddReference latest: %v", err)
	}

	return &crossProjectIsolationFixture{store: store, badgerStore: badgerStore, vb: vb, ns: ns, embedder: embedder, projectA: projectA, projectB: projectB, dep: dep}
}

func (f *crossProjectIsolationFixture) queryService() *query.Service {
	return query.New(f.store, f.badgerStore, f.vb, &embedding.Prompted{Embedder: f.embedder}, f.ns, f.vb.Name())
}

func TestModeProjectStructurallyExcludesOtherProjectsVersion(t *testing.T) {
	f := newCrossProjectIsolationFixture(t)
	svc := f.queryService()
	ctx := context.Background()

	for _, tc := range []struct {
		name         string
		projectID    string
		wantVersion  string
		otherVersion string
	}{
		{"project A", f.projectA, "v1.0.0", "v2.0.0"},
		{"project B", f.projectB, "v2.0.0", "v1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Query text semantically close to the OTHER version's unique
			// content — stress-testing that filtering, not just relevance
			// ranking, is what excludes it.
			queries := []string{"gopher-burrow-marker", "falcon-cascade-token", "widget behavior"}
			for _, q := range queries {
				result, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: tc.projectID, Dependency: f.dep.Name, Text: q, Mode: query.ModeProject})
				if err != nil {
					t.Fatalf("SearchKnowledge(%q): %v", q, err)
				}
				for _, c := range result.Chunks {
					if c.Version != tc.wantVersion {
						t.Errorf("query %q: chunk version = %q, want only %q — never %q (structural leak, not a ranking issue)", q, c.Version, tc.wantVersion, tc.otherVersion)
					}
				}
			}
		})
	}
}

func TestModeCompareAndAllRetainedBehaveAsDocumented(t *testing.T) {
	f := newCrossProjectIsolationFixture(t)
	svc := f.queryService()
	ctx := context.Background()

	// ModeCompare (project A's v1.0.0 vs latest v2.0.0) is documented to
	// merge BOTH versions' results — not isolated by design, unlike
	// ModeProject. Confirm it does exactly that, and that every returned
	// chunk's Version field is honest about which version it actually
	// came from (no cross-labeling).
	compareResult, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: f.projectA, Dependency: f.dep.Name, Text: "widget", Mode: query.ModeCompare})
	if err != nil {
		t.Fatalf("SearchKnowledge(ModeCompare): %v", err)
	}
	seenVersions := map[string]bool{}
	for _, c := range compareResult.Chunks {
		seenVersions[c.Version] = true
		if c.Version != "v1.0.0" && c.Version != "v2.0.0" {
			t.Errorf("ModeCompare chunk has unexpected version %q", c.Version)
		}
	}
	if !seenVersions["v1.0.0"] || !seenVersions["v2.0.0"] {
		t.Errorf("ModeCompare = versions %v, want both v1.0.0 (project) and v2.0.0 (latest) — this mode intentionally merges them", seenVersions)
	}

	// ModeAllRetained: ecosystem-wide, not project-scoped, per
	// docs/features/query-serving.md — confirm it stays dependency-scoped
	// (both retained versions of example.com/widget, correctly labeled),
	// not that it excludes anything the way ModeProject does.
	allResult, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: f.projectA, Dependency: f.dep.Name, Text: "widget", Mode: query.ModeAllRetained})
	if err != nil {
		t.Fatalf("SearchKnowledge(ModeAllRetained): %v", err)
	}
	seenVersions = map[string]bool{}
	for _, c := range allResult.Chunks {
		seenVersions[c.Version] = true
		if c.Dependency != f.dep.Name {
			t.Errorf("ModeAllRetained returned a chunk for dependency %q, want only %q", c.Dependency, f.dep.Name)
		}
	}
	if !seenVersions["v1.0.0"] || !seenVersions["v2.0.0"] {
		t.Errorf("ModeAllRetained = versions %v, want both retained versions", seenVersions)
	}
}

func TestRetentionKeepsBothVersionsWhileBothProjectsReference(t *testing.T) {
	f := newCrossProjectIsolationFixture(t)
	ctx := context.Background()

	candidates, err := retention.PlanGC(ctx, f.store, f.vb.Name(), 0, time.Now())
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	for _, c := range candidates {
		if c.Package == f.dep.Name {
			t.Errorf("PlanGC candidate %+v — neither version should be GC-eligible while both projects still reference their own version", c)
		}
	}
}

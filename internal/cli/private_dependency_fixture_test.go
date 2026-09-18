package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/query"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/source/git"
)

// VALID-004: a private, purely-local Go module with a real, source-
// breaking v1->v2 API change — the strongest concrete evidence for
// "wrong-version contamination is a checkable failure mode, not an
// abstract one." Zero public registry/network involvement: the
// manifest's git source URL is a local filesystem path, so acquisition
// is structurally incapable of reaching a public registry or network at
// all (a local `git clone` of a local path never makes a network call).

const v1ConnectSignature = "func Connect(addr string) (*Client, error)"
const v2ConnectSignature = "func Connect(addr string, opts ...Option) (*Client, error)"

// The real, compiling, source-incompatible signatures above live in the
// fixture's .go source and are what actually makes v1/v2 API-
// incompatible. Search results only carry the extracted doc *comment*
// text (query.ResultChunk.Content) — the raw signature is parsed and
// stored as chunk metadata (internal/normalize/godoc's
// symbolObject/"signature") but never surfaced through
// query.ResultChunk today, a separate, smaller finding this ticket
// notes rather than fixes (see its own post-implementation note). These
// two doc-text markers are what's actually retrievable and checkable.
const v1ConnectDocMarker = "using the v1 calling convention"
const v2ConnectDocMarker = "using variadic options (the v2"

// privateDependencyFixture builds a two-tag local git repo with a real,
// compiling, source-incompatible v1/v2 API change, and two projects
// resolving one version each — mirrors crossProjectIsolationFixture's
// shape (VALID-002) but with a real Go API break instead of just
// different prose content, and content shaped for the godoc normalizer
// (real .go source with doc comments) rather than README.md/markdown.
type privateDependencyFixture struct {
	store       *bboltstore.Store
	badgerStore *badgerstore.Store
	vb          *backendtest.Backend
	ns          backend.Namespace
	embedder    *atomicPromotionFakeEmbedder
	projectV1   string
	projectV2   string
	dep         domain.Dependency
	sourceURL   string // the manifest's git source URL — a local filesystem path, never a network URL
}

func newPrivateDependencyFixture(t *testing.T) *privateDependencyFixture {
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
	depRoot := filepath.Join(base, "breaking-lib")
	writeFile(t, depRoot, "lib.go", `// Package breakinglib is a fixture Go module for VALID-004.
package breakinglib

// Client represents an open connection.
type Client struct{}

// Connect opens a connection to addr using the v1 calling convention.
`+v1ConnectSignature+` { return &Client{}, nil }
`)
	atomicPromotionRunGit(t, depRoot, "init", "-q", "-b", "main")
	atomicPromotionRunGit(t, depRoot, "add", ".")
	atomicPromotionRunGit(t, depRoot, "commit", "-q", "-m", "v1.0.0")
	atomicPromotionRunGit(t, depRoot, "tag", "v1.0.0")

	writeFile(t, depRoot, "lib.go", `// Package breakinglib is a fixture Go module for VALID-004.
package breakinglib

// Client represents an open connection.
type Client struct{}

// Option configures a Connect call in the v2 calling convention.
type Option func(*Client)

// Connect opens a connection to addr using variadic options (the v2
// calling convention) — a real, source-incompatible rename from v1's
// two-argument signature.
`+v2ConnectSignature+` { return &Client{}, nil }
`)
	atomicPromotionRunGit(t, depRoot, "add", ".")
	atomicPromotionRunGit(t, depRoot, "commit", "-q", "-m", "v2.0.0")
	atomicPromotionRunGit(t, depRoot, "tag", "v2.0.0")

	regDir := t.TempDir()
	// depRoot is a plain local filesystem path, never a github.com/https
	// URL — acquisition here is structurally incapable of reaching a
	// public registry or the network.
	manifest := fmt.Sprintf(atomicPromotionManifestYAML, depRoot)
	writeFile(t, regDir, "manifest.yaml", strings.ReplaceAll(manifest, "example.com/widget", "example.com/breaking-lib"))
	reg, err := registry.NewLoader(regDir, "").Load(ctx)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	gitCache := git.NewCache(t.TempDir())
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	embedder := &atomicPromotionFakeEmbedder{dims: 4}
	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/breaking-lib"}

	const projectV1 = "proj_v1"
	const projectV2 = "proj_v2"
	for _, tc := range []struct {
		projectID string
		version   string
	}{{projectV1, "v1.0.0"}, {projectV2, "v2.0.0"}} {
		depVersion := domain.DependencyVersion{Dependency: dep, Version: tc.version}
		action := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: tc.projectID, Dependency: depVersion}
		if err := syncVersion(ctx, store, badgerStore, gitCache, embedder, vb, ns, reg, action, false); err != nil {
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
	}

	return &privateDependencyFixture{store: store, badgerStore: badgerStore, vb: vb, ns: ns, embedder: embedder, projectV1: projectV1, projectV2: projectV2, dep: dep, sourceURL: depRoot}
}

func (f *privateDependencyFixture) queryService() *query.Service {
	return query.New(f.store, f.badgerStore, f.vb, f.embedder, f.ns, f.vb.Name())
}

func TestPrivateDependencyReturnsOnlyResolvedVersionsSignature(t *testing.T) {
	f := newPrivateDependencyFixture(t)
	svc := f.queryService()
	ctx := context.Background()

	v1Result, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: f.projectV1, Dependency: f.dep.Name, Text: "how do I call Connect", Mode: query.ModeProject})
	if err != nil {
		t.Fatalf("SearchKnowledge (v1 project): %v", err)
	}
	assertContainsSignature(t, v1Result, v1ConnectDocMarker, v2ConnectDocMarker, "project resolving v1.0.0")

	v2Result, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: f.projectV2, Dependency: f.dep.Name, Text: "how do I call Connect", Mode: query.ModeProject})
	if err != nil {
		t.Fatalf("SearchKnowledge (v2 project): %v", err)
	}
	assertContainsSignature(t, v2Result, v2ConnectDocMarker, v1ConnectDocMarker, "project resolving v2.0.0")
}

// TestPrivateDependencyFixtureUsesNoPublicRegistryOrNetworkURL is a
// structural check, not a network-interception one: the manifest's git
// source is a plain local filesystem path (see newPrivateDependencyFixture),
// never a github.com/https URL — acquisition here is incapable of
// reaching a public registry or the network at all, by construction, so
// "private dependency" needed zero special-casing anywhere in
// acquisition.
func TestPrivateDependencyFixtureUsesNoPublicRegistryOrNetworkURL(t *testing.T) {
	f := newPrivateDependencyFixture(t)
	if strings.Contains(f.sourceURL, "://") || strings.HasPrefix(f.sourceURL, "git@") {
		t.Errorf("fixture source URL %q looks like a network URL, want a plain local filesystem path", f.sourceURL)
	}
	if !filepath.IsAbs(f.sourceURL) {
		t.Errorf("fixture source URL %q is not an absolute local path", f.sourceURL)
	}
}

func assertContainsSignature(t *testing.T, result query.SearchResult, want, mustNotContain, label string) {
	t.Helper()
	if len(result.Chunks) == 0 {
		t.Fatalf("%s: SearchKnowledge returned zero chunks", label)
	}
	found := false
	for _, c := range result.Chunks {
		if strings.Contains(c.Content, want) {
			found = true
		}
		if strings.Contains(c.Content, mustNotContain) {
			t.Errorf("%s: chunk content contains the OTHER version's signature %q — wrong-version contamination:\n%s", label, mustNotContain, c.Content)
		}
	}
	if !found {
		t.Errorf("%s: no chunk contains the expected signature %q", label, want)
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/planner"
)

func TestSyncDryRunPerformsNoWrites(t *testing.T) {
	isolateEnv(t)
	noAmbientSync(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "ADD_REFERENCE") {
		t.Errorf("dry-run output missing plan content:\n%s", out.String())
	}

	// No reference should have been recorded — dry-run must not write.
	// The daemon still holds the control store's exclusive lock; release
	// it before reading the store directly.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	refs, err := store.ListAllReferences(context.Background())
	if err != nil {
		t.Fatalf("ListAllReferences: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("ListAllReferences after dry-run = %+v, want none (no state written)", refs)
	}
}

func TestSyncOfflineSkipsSyncVersionButRecordsReference(t *testing.T) {
	isolateEnv(t)
	noAmbientSync(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	root := scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync", "--offline"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --offline: %v", err)
	}
	if !strings.Contains(out.String(), "SKIP (offline)") {
		t.Errorf("offline sync output missing skip line:\n%s", out.String())
	}

	// Find the project ID that was scanned. The daemon still holds the
	// control store's exclusive lock; release it first.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var projectID string
	for _, p := range projects {
		if p.Root == root {
			projectID = p.ID
		}
	}
	if projectID == "" {
		t.Fatalf("scanned project %s not found among %+v", root, projects)
	}

	// Reference bookkeeping doesn't need the network, so it must still
	// have happened even in offline mode.
	refs, err := store.ListProjectReferences(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListProjectReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].Package != "example.com/foo" || refs[0].Version == "" {
		t.Errorf("ListProjectReferences = %+v, want one reference to example.com/foo with a version", refs)
	}
}

func TestSyncNoOpAfterOfflineSyncPerformsNoFurtherReferenceChanges(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	first := NewRootCmd()
	first.SetArgs([]string{"sync", "--offline"})
	first.SetOut(new(bytes.Buffer))
	if err := first.Execute(); err != nil {
		t.Fatalf("first sync --offline: %v", err)
	}

	// Second run should be a NOOP for the reference (nothing changed) —
	// still reports the SYNC_VERSION as skipped (offline), since sync
	// state isn't accounted for without a real backend, but must not
	// error or duplicate reference records.
	second := NewRootCmd()
	second.SetArgs([]string{"sync", "--offline"})
	var out bytes.Buffer
	second.SetOut(&out)
	if err := second.Execute(); err != nil {
		t.Fatalf("second sync --offline: %v", err)
	}
}

// TestSyncNoOpPlanMakesNoNetworkCalls is the literal PLAN-003 acceptance
// criterion: a no-change sync performs zero backend/network writes. It
// seeds a VersionReference matching the resolved version exactly, so
// planner.Plan produces a NOOP (not a SYNC_VERSION), then runs `sync`
// WITHOUT --offline. If the embedder/vector-backend pipeline were built
// unconditionally (the bug this test guards against — an earlier version
// probed embedder.Dimensions on every non-offline run regardless of
// whether anything needed it), this would hang or fail trying to reach
// the configured Ollama/Qdrant endpoints; since nothing in the plan
// needs the pipeline, it must never be constructed at all.
func TestSyncNoOpPlanMakesNoNetworkCalls(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	root := scanDepFixture(t)

	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before reading and writing the store
	// directly. `sync` below auto-starts a fresh daemon of its own.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var project domain.Project
	for _, p := range projects {
		if p.Root == root {
			project = p
		}
	}
	if project.ID == "" {
		t.Fatalf("scanned project %s not found", root)
	}
	resolution, err := store.GetResolution(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if len(resolution.Dependencies) != 1 {
		t.Fatalf("resolution.Dependencies = %+v, want exactly 1", resolution.Dependencies)
	}
	dep := resolution.Dependencies[0]

	if err := store.AddReference(context.Background(), domain.VersionReference{
		ProjectID: project.ID,
		Ecosystem: dep.Dependency.Ecosystem,
		Package:   dep.Dependency.Name,
		Version:   dep.Version,
		Reason:    domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync"}) // deliberately NOT --offline
	var out bytes.Buffer
	cmd.SetOut(&out)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sync: %v\noutput: %s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not return within 10s — the pipeline was probably built (and is hanging on a network call) for a plan with no work to do")
	}
	if !strings.Contains(out.String(), "0 synced, 0 failed, 0 skipped") {
		t.Errorf("sync summary = %q, want a genuine no-op (0/0/0)", out.String())
	}
}

func TestSyncDependencyFilter(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync", "--offline", "--dependency", "does-not-exist"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --offline --dependency: %v", err)
	}
	if strings.Contains(out.String(), "example.com/foo") {
		t.Errorf("--dependency filter should have excluded example.com/foo:\n%s", out.String())
	}
}

// TestSyncReportsStructuredErrorForUnreachableEmbeddingBackend is
// WATCH-014's regression test: a sync that hits a SYNC_VERSION action
// against a dead embedding endpoint must report the daemon's own
// actionable "embedding backend unreachable" message, not a raw
// connection-refused error bubbled up from inside buildEmbedder.
func TestSyncReportsStructuredErrorForUnreachableEmbeddingBackend(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	deadEndpointsConfig(t, nil)
	scanDepFixture(t)

	c, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	waitFor(t, "embedding readiness to report unreachable", func() bool {
		h, err := c.Health(context.Background())
		return err == nil && h.EmbeddingState == "unreachable"
	})

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	// A failed sync action makes `sync` exit non-zero (see runSync) —
	// expected here, since the embedding backend is unreachable. The
	// assertion that matters is what got streamed to out, not the exit.
	if err := cmd.Execute(); err == nil {
		t.Fatal("sync succeeded despite an unreachable embedding backend, want a failed action")
	}
	if !strings.Contains(out.String(), "embedding backend unreachable") {
		t.Errorf("sync output = %q, want it to report the structured embedding-unreachable message instead of a raw connection error", out.String())
	}
}

// TestSyncReportsStructuredErrorForUnreachableVectorBackend is
// WATCH-015's regression test, mirroring WATCH-014's own: a sync that
// hits a SYNC_VERSION action against a dead vector endpoint (with a
// *reachable* embedding endpoint, to isolate which readiness check is
// actually firing) must report the daemon's own actionable "vector
// backend unreachable" message, not a raw dial error bubbled up from
// deep inside generation.Replicate.
func TestSyncReportsStructuredErrorForUnreachableVectorBackend(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{{"name": config.Default("/data").Embedding.Model + ":latest"}},
		})
	}))
	t.Cleanup(ollama.Close)
	deadVector := deadBackendURL(t)
	writeTestConfig(t, func(c *config.Config) {
		c.Embedding.Endpoint = ollama.URL
		c.Vector.Endpoint = deadVector
		c.Vector.Managed = false // exercise the plain report-only path, not WATCH-016's bootstrap
	})
	scanDepFixture(t)

	c, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	waitFor(t, "vector readiness to report unreachable", func() bool {
		h, err := c.Health(context.Background())
		return err == nil && h.VectorState == "unreachable"
	})
	waitFor(t, "embedding readiness to report ready", func() bool {
		h, err := c.Health(context.Background())
		return err == nil && h.EmbeddingState == "ready"
	})

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatal("sync succeeded despite an unreachable vector backend, want a failed action")
	}
	if !strings.Contains(out.String(), "vector backend unreachable") {
		t.Errorf("sync output = %q, want it to report the structured vector-unreachable message instead of a raw connection error", out.String())
	}
}

// TestFallbackManifestDerivesGithubURL covers REG-005: a Go dependency
// shaped like a direct github.com/<org>/<repo> import derives a
// single-source manifest instead of hitting the "no registry manifest"
// error a genuinely unmapped package still gets. No HTTP call should
// happen for this case — the fast path never needs one.
func TestFallbackManifestDerivesGithubURL(t *testing.T) {
	prev := vanityImportHTTPClient
	vanityImportHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("resolveVanityImport was called for a github.com-shaped module path")
		return nil, nil
	})}
	defer func() { vanityImportHTTPClient = prev }()

	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "github.com/dgraph-io/badger/v4"}
	m, ok := fallbackManifest(context.Background(), dep)
	if !ok {
		t.Fatal("fallbackManifest = false, want true for a github.com module path")
	}
	if len(m.Sources) != 1 || m.Sources[0].URL != "https://github.com/dgraph-io/badger" {
		t.Errorf("Sources = %+v, want one source at https://github.com/dgraph-io/badger", m.Sources)
	}
}

// roundTripFunc adapts a plain function to http.RoundTripper, so tests
// can redirect an http.Client at a local httptest.Server regardless of
// what host the request under test was built for.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// fakeVanityImportServer stands in for a real vanity-import host,
// serving body at any path (mirroring how a real ?go-get=1 request is
// answered regardless of the exact module subpath requested).
func fakeVanityImportServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// redirectVanityImportClient points vanityImportHTTPClient at srv for
// the duration of the test, regardless of the request's original host —
// exactly what a real go-import lookup does (GET https://<modulePath>),
// just served locally instead of over the real network.
func redirectVanityImportClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := vanityImportHTTPClient
	vanityImportHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		redirected := req.Clone(req.Context())
		redirected.URL.Scheme = "http"
		redirected.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(redirected)
	})}
	t.Cleanup(func() { vanityImportHTTPClient = prev })
}

// TestFallbackManifestResolvesVanityImportPath covers REG-008: a Go
// module path with no github.com-shaped prefix, but a real go-import
// meta tag, still derives a fallback manifest — the exact live gap
// found running github.com/spf13/cobra's own transitive dependencies
// (go.yaml.in/yaml/v3) through ragctl end to end.
func TestFallbackManifestResolvesVanityImportPath(t *testing.T) {
	srv := fakeVanityImportServer(t, `<html><head>
<meta name="go-import" content="example.vanity/pkg git https://github.com/example/pkg">
</head></html>`)
	redirectVanityImportClient(t, srv)

	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.vanity/pkg"}
	m, ok := fallbackManifest(context.Background(), dep)
	if !ok {
		t.Fatal("fallbackManifest = false, want true for a resolvable vanity import path")
	}
	if len(m.Sources) != 1 || m.Sources[0].URL != "https://github.com/example/pkg" {
		t.Errorf("Sources = %+v, want one source at https://github.com/example/pkg", m.Sources)
	}
}

// TestFallbackManifestResolvesVanityImportSubpackage covers a subpackage
// import path (root != the full module path) — exactly go.yaml.in/yaml/v3's
// own shape, where the go-import root is go.yaml.in/yaml.
func TestFallbackManifestResolvesVanityImportSubpackage(t *testing.T) {
	srv := fakeVanityImportServer(t, `<meta name="go-import" content="example.vanity/pkg git https://github.com/example/pkg" />`)
	redirectVanityImportClient(t, srv)

	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.vanity/pkg/v3"}
	m, ok := fallbackManifest(context.Background(), dep)
	if !ok {
		t.Fatal("fallbackManifest = false, want true for a subpackage of a resolvable vanity import root")
	}
	if len(m.Sources) != 1 || m.Sources[0].URL != "https://github.com/example/pkg" {
		t.Errorf("Sources = %+v, want one source at https://github.com/example/pkg", m.Sources)
	}
}

func TestFallbackManifestRejectsNonGitVanityImport(t *testing.T) {
	srv := fakeVanityImportServer(t, `<meta name="go-import" content="example.vanity/pkg bzr https://example.vanity/pkg">`)
	redirectVanityImportClient(t, srv)

	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.vanity/pkg"}
	if _, ok := fallbackManifest(context.Background(), dep); ok {
		t.Error("fallbackManifest = true for a non-git go-import VCS, want false (ragctl can only acquire from git)")
	}
}

func TestFallbackManifestRejectsNoGoImportTag(t *testing.T) {
	srv := fakeVanityImportServer(t, `<html><body>not a go-gettable page</body></html>`)
	redirectVanityImportClient(t, srv)

	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.vanity/pkg"}
	if _, ok := fallbackManifest(context.Background(), dep); ok {
		t.Error("fallbackManifest = true for a page with no go-import tag, want false")
	}
}

func TestFallbackManifestRejectsNonGoEcosystems(t *testing.T) {
	dep := domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "some-package"}
	if _, ok := fallbackManifest(context.Background(), dep); ok {
		t.Error("fallbackManifest = true for a non-Go ecosystem, want false")
	}
}

// syncVersionAction is a small fixture helper for bumpActionToFront's
// tests — a bare-minimum SYNC_VERSION action naming dep, nothing else
// about it matters for reordering logic.
func syncVersionAction(dep string) planner.Action {
	return planner.Action{
		Kind:       planner.ActionSyncVersion,
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Name: dep}},
	}
}

func TestBumpActionToFrontMovesNamedDependencyToFront(t *testing.T) {
	queue := []planner.Action{syncVersionAction("a"), syncVersionAction("b"), syncVersionAction("c")}
	got := bumpActionToFront(queue, "c")
	want := []string{"c", "a", "b"}
	for i, a := range got {
		if a.Dependency.Dependency.Name != want[i] {
			t.Errorf("bumpActionToFront order = %v, want %v", actionNames(got), want)
			break
		}
	}
}

func TestBumpActionToFrontAlreadyAtFrontIsNoOp(t *testing.T) {
	queue := []planner.Action{syncVersionAction("a"), syncVersionAction("b")}
	got := bumpActionToFront(queue, "a")
	if actionNames(got)[0] != "a" || actionNames(got)[1] != "b" {
		t.Errorf("bumpActionToFront order = %v, want unchanged [a b]", actionNames(got))
	}
}

func TestBumpActionToFrontMissingDependencyIsNoOp(t *testing.T) {
	queue := []planner.Action{syncVersionAction("a"), syncVersionAction("b")}
	got := bumpActionToFront(queue, "not-in-queue")
	if len(got) != 2 || actionNames(got)[0] != "a" || actionNames(got)[1] != "b" {
		t.Errorf("bumpActionToFront order = %v, want unchanged [a b] for a dependency not in the queue", actionNames(got))
	}
}

func TestBumpActionToFrontSkipsNonSyncVersionActionsWithSameName(t *testing.T) {
	// An ADD_REFERENCE action can share a dependency name with a later
	// SYNC_VERSION one — bumping must target the SYNC_VERSION action
	// specifically, not whatever action happens to match by name first.
	ref := planner.Action{Kind: planner.ActionAddReference, Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Name: "a"}}}
	queue := []planner.Action{ref, syncVersionAction("b"), syncVersionAction("a")}
	got := bumpActionToFront(queue, "a")
	if got[0].Kind != planner.ActionSyncVersion || got[0].Dependency.Dependency.Name != "a" {
		t.Errorf("bumpActionToFront = %+v, want the SYNC_VERSION action for %q moved to front, not the ADD_REFERENCE one", got, "a")
	}
}

func actionNames(actions []planner.Action) []string {
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = a.Dependency.Dependency.Name
	}
	return names
}

// TestFallbackManifestScopesMonorepoModuleToItsSubdir is the POINT-002
// regression: a module inside a monorepo (cloud.google.com/go/billing,
// root cloud.google.com/go) must index only its own directory, not the
// repo root shared by every sibling module.
func TestFallbackManifestScopesMonorepoModuleToItsSubdir(t *testing.T) {
	srv := fakeVanityImportServer(t, `<meta name="go-import" content="example.vanity/mono git https://github.com/example/mono">`)
	redirectVanityImportClient(t, srv)

	cases := map[string]string{
		"example.vanity/mono":            "",
		"example.vanity/mono/billing":    "billing",
		"example.vanity/mono/a/b/v3":     "a/b",
		"example.vanity/mono/v2":         "",
		"github.com/org/repo":            "",
		"github.com/org/repo/v4":         "",
		"github.com/org/repo/sub/pkg/v2": "sub/pkg",
	}
	for name, want := range cases {
		m, ok := fallbackManifest(context.Background(), domain.Dependency{Ecosystem: domain.EcosystemGo, Name: name})
		if !ok {
			t.Fatalf("%s: fallbackManifest = false", name)
		}
		if got := m.Sources[0].Subdir; got != want {
			t.Errorf("%s: Subdir = %q, want %q", name, got, want)
		}
	}
}

func planActionsFor(names ...string) []planner.Action {
	var actions []planner.Action
	for _, n := range names {
		actions = append(actions, planner.Action{
			Kind:       planner.ActionSyncVersion,
			Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: n}, Version: "v1.0.0"},
		})
	}
	return actions
}

// TestFlattenActionsFiltersToTheRequestedSet is SCOPE-003's RunSync-side
// check: a set of names keeps exactly those dependencies, in plan order;
// an empty set keeps everything; unknown names are a quiet no-op.
func TestFlattenActionsFiltersToTheRequestedSet(t *testing.T) {
	plans := []projectPlan{{Actions: planActionsFor("a", "b", "c", "d")}}
	names := func(actions []planner.Action) []string {
		var out []string
		for _, a := range actions {
			out = append(out, a.Dependency.Dependency.Name)
		}
		return out
	}

	if got := names(flattenActions(plans, []string{"c", "a"}, io.Discard)); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("set {c,a} kept %v, want [a c] in plan order", got)
	}
	if got := names(flattenActions(plans, nil, io.Discard)); len(got) != 4 {
		t.Errorf("empty set kept %v, want everything", got)
	}
	if got := flattenActions(plans, []string{"nope"}, io.Discard); len(got) != 0 {
		t.Errorf("unknown name kept %d actions, want none", len(got))
	}
}

// TestFallbackManifestPinsTheVersionTag: a fallback source must resolve
// the dependency version's own tag — "v1.2.3" for a repo-root module,
// "<subdir>/v1.2.3" for a monorepo submodule — never a branch head.
func TestFallbackManifestPinsTheVersionTag(t *testing.T) {
	srv := fakeVanityImportServer(t, `<meta name="go-import" content="example.vanity/mono git https://github.com/example/mono">`)
	redirectVanityImportClient(t, srv)

	cases := map[string]string{
		"example.vanity/mono":         "v${version}",
		"example.vanity/mono/billing": "billing/v${version}",
		"example.vanity/mono/a/b/v3":  "a/b/v${version}",
		"github.com/org/repo":         "v${version}",
		"github.com/org/repo/sub":     "sub/v${version}",
	}
	for name, want := range cases {
		m, ok := fallbackManifest(context.Background(), domain.Dependency{Ecosystem: domain.EcosystemGo, Name: name})
		if !ok {
			t.Fatalf("%s: fallbackManifest = false", name)
		}
		if got := m.Sources[0].Ref; got != want {
			t.Errorf("%s: Ref = %q, want %q", name, got, want)
		}
		if m.Sources[0].Ref == "HEAD" {
			t.Errorf("%s: fallback source still resolves HEAD", name)
		}
	}
}

func fakeNpmRegistryServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func redirectNpmRegistryClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := npmRegistryHTTPClient
	npmRegistryHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		redirected := req.Clone(req.Context())
		redirected.URL.Scheme = "http"
		redirected.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(redirected)
	})}
	t.Cleanup(func() { npmRegistryHTTPClient = prev })
}

func TestNpmGitURLNormalizesEveryShape(t *testing.T) {
	cases := map[string]string{
		"git+https://github.com/npm/node-semver.git":   "https://github.com/npm/node-semver",
		"git://github.com/npm/node-semver.git":         "https://github.com/npm/node-semver",
		"https://github.com/npm/node-semver.git":       "https://github.com/npm/node-semver",
		"git+ssh://git@github.com/npm/node-semver.git": "https://github.com/npm/node-semver",
		"github:npm/node-semver":                       "https://github.com/npm/node-semver",
		"npm/node-semver":                              "https://github.com/npm/node-semver",
	}
	for raw, want := range cases {
		got, ok := npmGitURL(raw)
		if !ok || got != want {
			t.Errorf("npmGitURL(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	if _, ok := npmGitURL("not a url at all !!"); ok {
		t.Error("npmGitURL accepted garbage")
	}
}

func TestNpmRepositoryObjectShapeWithDirectory(t *testing.T) {
	srv := fakeNpmRegistryServer(t, `{"repository":{"type":"git","url":"git+https://github.com/eslint/js.git","directory":"packages/eslint-visitor-keys"}}`)
	redirectNpmRegistryClient(t, srv)

	url, subdir, ok := npmRepository(context.Background(), "eslint-visitor-keys")
	if !ok || url != "https://github.com/eslint/js" || subdir != "packages/eslint-visitor-keys" {
		t.Errorf("npmRepository = %q, %q, %v", url, subdir, ok)
	}
}

func TestNpmRepositoryStringShorthand(t *testing.T) {
	srv := fakeNpmRegistryServer(t, `{"repository":"github:tj/commander.js"}`)
	redirectNpmRegistryClient(t, srv)

	url, subdir, ok := npmRepository(context.Background(), "commander")
	if !ok || url != "https://github.com/tj/commander.js" || subdir != "" {
		t.Errorf("npmRepository = %q, %q, %v", url, subdir, ok)
	}
}

func TestNpmRepositoryMissingIsACleanMiss(t *testing.T) {
	srv := fakeNpmRegistryServer(t, `{}`)
	redirectNpmRegistryClient(t, srv)
	if _, _, ok := npmRepository(context.Background(), "whatever"); ok {
		t.Error("npmRepository succeeded with no repository field")
	}
}

func TestNpmTagCandidatesForASinglePackageRepo(t *testing.T) {
	got := npmTagCandidates("commander", "")
	want := []string{"v${version}", "${version}", "commander@${version}", "commander-v${version}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("npmTagCandidates = %v, want %v", got, want)
	}
}

// TestNpmTagCandidatesForAMonorepoPackageExcludesBareTags is the direct
// regression for the live-found eslint-visitor-keys bug: a bare version
// tag in a monorepo can belong to an entirely different package sharing
// the repo (verified live: github.com/eslint/js's "v3.4.3" is ESLint
// core's own unrelated 2016 release, not eslint-visitor-keys 3.4.3) — so
// once a monorepo directory is known, only package-scoped tag shapes are
// ever offered as candidates.
func TestNpmTagCandidatesForAMonorepoPackageExcludesBareTags(t *testing.T) {
	got := npmTagCandidates("eslint-visitor-keys", "packages/eslint-visitor-keys")
	want := []string{"eslint-visitor-keys@${version}", "eslint-visitor-keys-v${version}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("npmTagCandidates = %v, want %v (no bare version tags for a monorepo package)", got, want)
	}
}

func TestNpmFallbackManifestBuildsSourceWithSubdirAndTemplates(t *testing.T) {
	srv := fakeNpmRegistryServer(t, `{"repository":{"type":"git","url":"git+https://github.com/eslint/js.git","directory":"packages/eslint-visitor-keys"}}`)
	redirectNpmRegistryClient(t, srv)

	m, ok := fallbackManifest(context.Background(), domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "eslint-visitor-keys"})
	if !ok {
		t.Fatal("fallbackManifest = false")
	}
	src := m.Sources[0]
	if src.URL != "https://github.com/eslint/js" || src.Subdir != "packages/eslint-visitor-keys" {
		t.Errorf("source = %+v", src)
	}
	if len(src.RefTemplates) != 2 || src.Ref != "" {
		t.Errorf("source RefTemplates=%v Ref=%q, want the 2 package-scoped templates only (no bare version tags — this dependency's own repository is a monorepo) and no singular Ref", src.RefTemplates, src.Ref)
	}
	for _, tmpl := range src.RefTemplates {
		if !strings.Contains(tmpl, "eslint-visitor-keys") {
			t.Errorf("RefTemplates contains a bare, unscoped template %q for a monorepo package", tmpl)
		}
	}
}

func TestNpmFallbackManifestFalseWithoutARepository(t *testing.T) {
	srv := fakeNpmRegistryServer(t, `{}`)
	redirectNpmRegistryClient(t, srv)
	if _, ok := fallbackManifest(context.Background(), domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "whatever"}); ok {
		t.Error("fallbackManifest = true for an npm package with no repository field")
	}
}

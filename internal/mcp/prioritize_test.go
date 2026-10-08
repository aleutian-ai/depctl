package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/query"
	"github.com/aleutian-ai/depctl/internal/symbolgraph"
)

func goDep(name string) domain.DependencyVersion {
	return domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: name}, Version: "v1.0.0"}
}

func TestMatchImportsUsesLongestModulePrefix(t *testing.T) {
	deps := []query.ProjectDependency{
		{Dependency: goDep("cloud.example.com/go")},
		{Dependency: goDep("cloud.example.com/go/billing")},
		{Dependency: goDep("example.com/alpha")},
		{Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemPython, Name: "example.com/pyonly"}}},
	}
	imports := []string{
		"cloud.example.com/go/billing/apiv1", // belongs to billing, not to the shorter root module
		"cloud.example.com/go/storage",       // only the root module owns it
		"cloud.example.com/goblin",           // shares a prefix as text, but is not a sub-path of .../go
		"example.com/alpha",
		"example.com/alpha/internal/x",
		"example.com/pyonly",
		"fmt",
		"net/http",
	}
	var names []string
	for _, m := range matchImports(imports, deps) {
		names = append(names, m.Dependency.Dependency.Name)
	}
	want := []string{"cloud.example.com/go", "cloud.example.com/go/billing", "example.com/alpha"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("matched %v, want %v (unique, longest prefix, Go only, stdlib excluded)", names, want)
	}
}

func TestParseGoImportsReadsOnlyTheImportBlock(t *testing.T) {
	imports, ok := parseGoImports([]byte("package p\n\nimport (\n\t\"fmt\"\n\tx \"example.com/alpha\"\n\t_ \"example.com/beta\"\n)\n\nfunc broken( {{{ not valid go"))
	if !ok || !reflect.DeepEqual(imports, []string{"fmt", "example.com/alpha", "example.com/beta"}) {
		t.Errorf("imports = %v (ok %v), want all three regardless of the broken body", imports, ok)
	}
	if _, ok := parseGoImports([]byte("this is not go at all")); ok {
		t.Error("garbage parsed as Go")
	}
}

type prioritizeFixture struct {
	env  *testEnv
	root string
	file string
}

// newPrioritizeFixture registers proj_1 with alpha (already synced), beta
// and gamma (not synced), and a Go file importing all three plus stdlib.
func newPrioritizeFixture(t *testing.T) *prioritizeFixture {
	t.Helper()
	env := newTestEnv(t)
	root := t.TempDir()
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: root}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		goDep("example.com/alpha"), goDep("example.com/beta"), goDep("example.com/gamma"),
	}}
	env.seedChunk(t, domain.EcosystemGo, "example.com/alpha", "v1.0.0", "gen_alpha", "chk_alpha", "alpha docs")

	file := filepath.Join(root, "main.go")
	src := "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\n\t\"example.com/alpha\"\n\t\"example.com/beta/sub\"\n\t\"example.com/gamma\"\n)\n\nfunc main() {}\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return &prioritizeFixture{env: env, root: root, file: file}
}

func (f *prioritizeFixture) seed(t *testing.T, name string) {
	t.Helper()
	short := filepath.Base(name)
	f.env.seedChunk(t, domain.EcosystemGo, name, "v1.0.0", "gen_"+short, "chk_"+short, short+" docs")
}

func fastWaits(t *testing.T) {
	t.Helper()
	prevBound, prevPoll := mcpSyncWaitBound, readinessPollInterval
	mcpSyncWaitBound, readinessPollInterval = 300*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { mcpSyncWaitBound, readinessPollInterval = prevBound, prevPoll })
}

// TestPrioritizeFileBuildsExactlyTheUnsyncedImportsAsOneSet is SCOPE-004's
// core check: the file's imports become exactly the not-yet-synced
// dependencies, requested together in a single scoped sync — not one
// request per import, and never a full untargeted sync.
func TestPrioritizeFileBuildsExactlyTheUnsyncedImportsAsOneSet(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	trigger := &fakeSyncTrigger{onSync: func() {
		f.seed(t, "example.com/beta")
		f.seed(t, "example.com/gamma")
	}}
	handler := prioritizeFileHandler(jitDeps{svc: f.env.svc, sync: trigger, enabled: true})

	_, out, err := handler(context.Background(), nil, PrioritizeFileIn{ProjectID: "proj_1", File: "main.go"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if trigger.calls != 1 || !reflect.DeepEqual(trigger.calledDependencies, []string{"example.com/beta", "example.com/gamma"}) {
		t.Errorf("sync called %d times with %v, want once with exactly [beta gamma] (alpha was already synced)", trigger.calls, trigger.calledDependencies)
	}
	if out.Imports != 5 || len(out.Matched) != 3 || out.StillBuilding {
		t.Errorf("out = %+v, want 5 imports, 3 matched, not still building", out)
	}
	for _, m := range out.Matched {
		if !m.Ready {
			t.Errorf("%s not ready after the sync finished", m.Name)
		}
	}
}

// TestPrioritizeFileBumpsInsteadOfStartingASyncWhenOneIsRunning: with a
// background sync already running, the imports jump its queue and no
// second sync is started.
func TestPrioritizeFileBumpsInsteadOfStartingASyncWhenOneIsRunning(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	trigger := &fakeSyncTrigger{}
	bumper := &fakePriorityBumper{bump: func(_ context.Context, _, dep string) (bool, error) {
		f.seed(t, dep) // the running sync gets to it
		return true, nil
	}}
	handler := prioritizeFileHandler(jitDeps{svc: f.env.svc, sync: trigger, priority: bumper, enabled: true})

	_, out, err := handler(context.Background(), nil, PrioritizeFileIn{ProjectID: "proj_1", File: f.file})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if trigger.called {
		t.Error("a second sync was started although a running one was bumped")
	}
	if out.StillBuilding || len(out.Matched) != 3 {
		t.Errorf("out = %+v, want all three ready", out)
	}
}

func TestPrioritizeFileReturnsStillBuildingWithinTheBound(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	trigger := newBlockingSyncTrigger()
	t.Cleanup(func() { close(trigger.release) })
	handler := prioritizeFileHandler(jitDeps{svc: f.env.svc, sync: trigger, enabled: true})

	start := time.Now()
	_, out, err := handler(context.Background(), nil, PrioritizeFileIn{ProjectID: "proj_1", File: "main.go"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !out.StillBuilding {
		t.Errorf("out = %+v, want still_building while the sync has not finished", out)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v, want it bounded by mcpSyncWaitBound", time.Since(start))
	}
	var pendingReady int
	for _, m := range out.Matched {
		if m.Ready {
			pendingReady++
		}
	}
	if pendingReady != 1 { // only alpha, which was already synced
		t.Errorf("%d matched dependencies ready, want just the already-synced alpha", pendingReady)
	}
}

func TestPrioritizeFileWithEverythingAlreadySyncedStartsNothing(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	f.seed(t, "example.com/beta")
	f.seed(t, "example.com/gamma")
	trigger := &fakeSyncTrigger{}
	handler := prioritizeFileHandler(jitDeps{svc: f.env.svc, sync: trigger, enabled: true})

	_, out, err := handler(context.Background(), nil, PrioritizeFileIn{ProjectID: "proj_1", File: "main.go"})
	if err != nil || trigger.called {
		t.Fatalf("err %v, sync called %v — want a quiet no-op", err, trigger.called)
	}
	if len(out.Matched) != 3 || out.StillBuilding {
		t.Errorf("out = %+v, want three ready matches", out)
	}
}

func TestPrioritizeFileQuietNoOpsAndRealErrors(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "broken.go"), []byte("not go"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "stdonly.go"), []byte("package p\n\nimport \"fmt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trigger := &fakeSyncTrigger{}
	handler := prioritizeFileHandler(jitDeps{svc: f.env.svc, sync: trigger, enabled: true})
	call := func(project, file string) (PrioritizeFileOut, error) {
		_, out, err := handler(context.Background(), nil, PrioritizeFileIn{ProjectID: project, File: file})
		return out, err
	}

	for _, file := range []string{"notes.txt", "broken.go", "stdonly.go"} {
		out, err := call("proj_1", file)
		if err != nil || len(out.Matched) != 0 || out.Note == "" {
			t.Errorf("%s: out %+v, err %v — want a quiet no-op with a note", file, out, err)
		}
	}
	if trigger.called {
		t.Error("a no-op file started a sync")
	}
	if _, err := call("proj_1", "does_not_exist.go"); err == nil {
		t.Error("a missing file is a real mistake and should be an error")
	}
	if _, err := call("proj_missing", "main.go"); !errors.Is(err, query.ErrProjectNotFound) && err == nil {
		t.Errorf("unknown project err = %v, want a project-not-found error", err)
	}

	disabled := prioritizeFileHandler(jitDeps{svc: f.env.svc, sync: trigger, enabled: false})
	if _, _, err := disabled(context.Background(), nil, PrioritizeFileIn{ProjectID: "proj_1", File: "main.go"}); err == nil {
		t.Error("with syncing disabled, a file needing builds should say so, not silently do nothing")
	}
}

// --- explain_call_site's just-in-time fallback ---

type notSyncedThenReady struct {
	dep    string
	synced func() bool
	bundle *symbolgraph.EvidenceBundle
	calls  int
}

func (r *notSyncedThenReady) ResolveEvidence(context.Context, string, symbolgraph.CallSite, string) (*symbolgraph.EvidenceBundle, error) {
	r.calls++
	if !r.synced() {
		return nil, &symbolgraph.NotSyncedError{Dependency: r.dep, Err: query.ErrNoActiveGeneration}
	}
	return r.bundle, nil
}

func TestExplainCallSiteBuildsAnUnsyncedDependencyThenRetries(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	var done atomic.Bool
	trigger := &fakeSyncTrigger{onSync: func() { f.seed(t, "example.com/beta"); done.Store(true) }}
	resolver := &notSyncedThenReady{
		dep:    "example.com/beta",
		synced: done.Load,
		bundle: &symbolgraph.EvidenceBundle{
			Symbol:     symbolgraph.ExternalSymbolRef{Ecosystem: "go", Module: "example.com/beta", Package: "beta", QualifiedName: "Do"},
			Dependency: goDep("example.com/beta"),
			Result:     query.SearchResult{Chunks: []query.ResultChunk{{ChunkID: "c1", Content: "Do does it"}}},
		},
	}
	handler := explainCallSiteHandler(resolver, jitDeps{svc: f.env.svc, sync: trigger, enabled: true})

	_, out, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "main.go", Line: 3, Column: 4})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.Symbol == nil || len(out.Chunks) != 1 {
		t.Errorf("out = %+v, want real evidence once the dependency was built (before this, a dead end)", out)
	}
	if !reflect.DeepEqual(trigger.calledDependencies, []string{"example.com/beta"}) || resolver.calls != 2 {
		t.Errorf("sync deps %v, resolver calls %d — want a sync for just beta, then exactly one retry", trigger.calledDependencies, resolver.calls)
	}
}

func TestExplainCallSiteStillBuildingIsNotAnError(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	trigger := newBlockingSyncTrigger()
	t.Cleanup(func() { close(trigger.release) })
	resolver := &notSyncedThenReady{dep: "example.com/beta", synced: func() bool { return false }}
	handler := explainCallSiteHandler(resolver, jitDeps{svc: f.env.svc, sync: trigger, enabled: true})

	_, out, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "main.go", Line: 3, Column: 4})
	if err != nil || !out.StillBuilding {
		t.Errorf("out %+v, err %v — want still_building, no error", out, err)
	}
}

func TestExplainCallSiteReportsTheOriginalErrorWhenTheSyncFails(t *testing.T) {
	fastWaits(t)
	f := newPrioritizeFixture(t)
	trigger := &fakeSyncTrigger{err: errors.New("clone failed")}
	resolver := &notSyncedThenReady{dep: "example.com/beta", synced: func() bool { return false }}
	handler := explainCallSiteHandler(resolver, jitDeps{svc: f.env.svc, sync: trigger, enabled: true})

	if _, _, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "main.go", Line: 3, Column: 4}); err == nil {
		t.Error("a failed JIT sync should fall back to the original, understood error")
	}
}

func TestExplainCallSiteWithSyncDisabledKeepsTheOldBehavior(t *testing.T) {
	f := newPrioritizeFixture(t)
	resolver := &notSyncedThenReady{dep: "example.com/beta", synced: func() bool { return false }}
	handler := explainCallSiteHandler(resolver, jitDeps{svc: f.env.svc, enabled: false})
	if _, _, err := handler(context.Background(), nil, ExplainCallSiteIn{ProjectID: "proj_1", File: "main.go", Line: 3, Column: 4}); err == nil {
		t.Error("with syncing disabled the unsynced error must still surface")
	}
	if resolver.calls != 1 {
		t.Errorf("resolver called %d times, want no retry when nothing could be built", resolver.calls)
	}
}

// TestPrioritizeFileToolIsCallableOverMCP proves registration, schema and
// JSON shape over a real in-memory client/server pair.
func TestPrioritizeFileToolIsCallableOverMCP(t *testing.T) {
	f := newPrioritizeFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := New(Deps{Query: f.env.svc, Sync: &fakeSyncTrigger{}, EnableSyncTool: true})

	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	res, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "prioritize_file", Arguments: map[string]any{"project_id": "proj_1", "file": "notes.txt"}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool: err %v, result %+v", err, res)
	}
}

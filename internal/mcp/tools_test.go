package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/query"
)

// --- minimal fakes, same shape as internal/query's own test fakes ---

type fakeEmbedder struct{ dims int }

func (f *fakeEmbedder) Name() string    { return "fake" }
func (f *fakeEmbedder) ModelID() string { return "fake-model" }
func (f *fakeEmbedder) Dimensions(ctx context.Context) (int, error) {
	return f.dims, nil
}
func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, f.dims)
		out[i][0] = 1
	}
	return out, nil
}

type fakeControlStore struct {
	// mu guards the maps a just-in-time sync's goroutine writes (via
	// seedChunk) while a handler is polling them.
	mu          sync.Mutex
	projects    map[string]domain.Project
	resolutions map[string]domain.Resolution
	active      map[string]domain.Generation
	refs        []domain.VersionReference
	generations []domain.Generation // every generation ever seeded, any state — see seedChunk
}

func newFakeControlStore() *fakeControlStore {
	return &fakeControlStore{projects: map[string]domain.Project{}, resolutions: map[string]domain.Resolution{}, active: map[string]domain.Generation{}}
}

var errNotFound = errors.New("not found")

func (s *fakeControlStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	return out, nil
}
func (s *fakeControlStore) GetProject(ctx context.Context, id string) (domain.Project, error) {
	p, ok := s.projects[id]
	if !ok {
		return domain.Project{}, errNotFound
	}
	return p, nil
}
func (s *fakeControlStore) GetResolution(ctx context.Context, projectID string) (domain.Resolution, error) {
	r, ok := s.resolutions[projectID]
	if !ok {
		return domain.Resolution{}, errNotFound
	}
	return r, nil
}
func (s *fakeControlStore) GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, backendName string) (domain.Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.active[string(ecosystem)+"|"+pkg+"|"+backendName]
	if !ok {
		return domain.Generation{}, errNotFound
	}
	return g, nil
}
func (s *fakeControlStore) ListReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.VersionReference, error) {
	var out []domain.VersionReference
	for _, r := range s.refs {
		if r.Ecosystem == ecosystem && r.Package == pkg && r.Version == version {
			out = append(out, r)
		}
	}
	return out, nil
}
func (s *fakeControlStore) ListAllReferences(ctx context.Context) ([]domain.VersionReference, error) {
	return s.refs, nil
}
func (s *fakeControlStore) ListGenerationsByDependencyVersion(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Generation
	for _, g := range s.generations {
		if g.Dependency.Dependency.Ecosystem == ecosystem && g.Dependency.Dependency.Name == pkg && g.Dependency.Version == version {
			out = append(out, g)
		}
	}
	return out, nil
}

type fakeDataStore struct {
	chunks  map[string]domain.Chunk
	objects map[string]domain.KnowledgeObject
}

func newFakeDataStore() *fakeDataStore {
	return &fakeDataStore{chunks: map[string]domain.Chunk{}, objects: map[string]domain.KnowledgeObject{}}
}
func (s *fakeDataStore) GetChunk(ctx context.Context, generationID, chunkID string) (domain.Chunk, error) {
	c, ok := s.chunks[generationID+"|"+chunkID]
	if !ok {
		return domain.Chunk{}, errNotFound
	}
	return c, nil
}
func (s *fakeDataStore) GetKnowledgeObject(ctx context.Context, id string) (domain.KnowledgeObject, error) {
	o, ok := s.objects[id]
	if !ok {
		return domain.KnowledgeObject{}, errNotFound
	}
	return o, nil
}
func (s *fakeDataStore) ListGenerationChunks(ctx context.Context, generationID string) ([]domain.Chunk, error) {
	return nil, nil
}

type fakeSyncTrigger struct {
	synced, failed, skipped int
	err                     error
	called                  bool
	calls                   int
	calledProjectID         string
	calledDependency        string   // dependencies joined by ","
	calledDependencies      []string // exactly what SyncProject received
	progressLines           []string // lines this fake "streams" via the progress callback
	onSync                  func()   // WATCH-019: simulates a real sync's side effect (an active generation appearing)
}

func (f *fakeSyncTrigger) SyncProject(ctx context.Context, projectID string, dependencies []string, progress func(line string)) (int, int, int, error) {
	f.called = true
	f.calls++
	f.calledProjectID = projectID
	f.calledDependency = strings.Join(dependencies, ",")
	f.calledDependencies = dependencies
	if progress != nil {
		for _, line := range f.progressLines {
			progress(line)
		}
	}
	if f.onSync != nil {
		f.onSync()
	}
	return f.synced, f.failed, f.skipped, f.err
}

// fakePriorityBumper is WATCH-020's fake PriorityBumper — bump, when
// set, decides the return value (and can perform a side effect, e.g.
// simulating the background sync actually finishing the dependency).
type fakePriorityBumper struct {
	bump             func(ctx context.Context, projectID, dependency string) (bool, error)
	called           bool
	calledProjectID  string
	calledDependency string
}

func (f *fakePriorityBumper) BumpSyncPriority(ctx context.Context, projectID, dependency string) (bool, error) {
	f.called = true
	f.calledProjectID = projectID
	f.calledDependency = dependency
	if f.bump != nil {
		return f.bump(ctx, projectID, dependency)
	}
	return false, nil
}

type fakeScanTrigger struct {
	ids           []string
	summary       string
	err           error
	called        bool
	calledRoot    string
	progressLines []string
}

func (f *fakeScanTrigger) ScanProject(ctx context.Context, root string, progress func(line string)) ([]string, string, error) {
	f.called = true
	f.calledRoot = root
	if progress != nil {
		for _, line := range f.progressLines {
			progress(line)
		}
	}
	return f.ids, f.summary, f.err
}

// testEnv wires a real query.Service against fakes — same approach as
// internal/query's own tests — so these tests exercise the real
// Service→backend/embedder call chain, not just handler plumbing.
type testEnv struct {
	control *fakeControlStore
	data    *fakeDataStore
	vb      *backendtest.Backend
	svc     *query.Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	control := newFakeControlStore()
	data := newFakeDataStore()
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	if err := vb.EnsureNamespace(context.Background(), ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	svc := query.New(control, data, vb, &fakeEmbedder{dims: 4}, ns, "qdrant")
	return &testEnv{control: control, data: data, vb: vb, svc: svc}
}

func (e *testEnv) seedChunk(t *testing.T, ecosystem domain.Ecosystem, pkg, version, generationID, chunkID, content string) {
	t.Helper()
	ctx := context.Background()
	err := e.vb.Upsert(ctx, backend.UpsertRequest{
		Namespace: "ragctl",
		Points: []backend.Point{{
			ID: chunkID, Vector: []float32{1, 0, 0, 0},
			Metadata: backend.PointMetadata{Ecosystem: string(ecosystem), Dependency: pkg, Version: version, Generation: generationID, SourceType: "git", Authority: 100},
		}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	e.data.chunks[generationID+"|"+chunkID] = domain.Chunk{ID: chunkID, Content: []byte(content)}
	gen := domain.Generation{
		ID:         generationID,
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: ecosystem, Name: pkg}, Version: version},
		State:      domain.GenActive,
	}
	e.control.mu.Lock()
	e.control.active[string(ecosystem)+"|"+pkg+"|qdrant"] = gen
	e.control.generations = append(e.control.generations, gen)
	e.control.mu.Unlock()
}

func TestSearchDependencyDocsHandlerReturnsChunksWithSecurityNote(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")

	handler := searchDependencyDocsHandler(env.svc, nil, false, nil)
	_, out, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(out.Chunks) != 1 || out.Chunks[0].Content != "grpc retry docs" {
		t.Errorf("out.Chunks = %+v, unexpected", out.Chunks)
	}
	if out.Note != securityNote {
		t.Errorf("out.Note = %q, want %q", out.Note, securityNote)
	}
	if out.Chunks[0].TrustClass != domain.TrustRepository {
		t.Errorf("out.Chunks[0].TrustClass = %q, want %q — this is the provenance signal an agent needs to weigh alongside authority", out.Chunks[0].TrustClass, domain.TrustRepository)
	}
}

// TestSearchDependencyDocsHandlerBreadcrumbRoundTripsThroughJSON is
// STRUCT-004's end-to-end acceptance criterion: SearchDependencyDocsOut's
// JSON round-trips with the breadcrumb field present and correctly
// populated for both a Markdown- and symbol-sourced result.
func TestSearchDependencyDocsHandlerBreadcrumbRoundTripsThroughJSON(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")
	env.data.chunks["gen_1|chk_1"] = domain.Chunk{
		ID: "chk_1", Content: []byte("grpc retry docs"),
		Metadata: map[string]string{"section_path": `["Authentication","Retries"]`},
	}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_2", "bucket docs")
	env.data.chunks["gen_1|chk_2"] = domain.Chunk{
		ID: "chk_2", Content: []byte("bucket docs"),
		Metadata: map[string]string{"symbol": "(*Tx).Bucket", "source_path": "tx.go"},
	}

	handler := searchDependencyDocsHandler(env.svc, nil, false, nil)
	_, out, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(out.Chunks) != 2 {
		t.Fatalf("out.Chunks = %+v, want 2", out.Chunks)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTripped SearchDependencyDocsOut
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	byChunkID := map[string]string{}
	for _, c := range roundTripped.Chunks {
		byChunkID[c.ChunkID] = c.Breadcrumb
	}
	if want := "google.golang.org/grpc@v1.67.0 > Authentication > Retries"; byChunkID["chk_1"] != want {
		t.Errorf("chk_1 breadcrumb = %q, want %q", byChunkID["chk_1"], want)
	}
	if want := "google.golang.org/grpc@v1.67.0 > tx.go > (*Tx).Bucket"; byChunkID["chk_2"] != want {
		t.Errorf("chk_2 breadcrumb = %q, want %q", byChunkID["chk_2"], want)
	}
}

// TestSearchDependencyDocsHandlerTriggersJITSyncOnMissingGeneration is
// WATCH-019's core regression test: a resolvable dependency with no
// active generation gets a sync scoped to exactly that dependency
// triggered automatically, and the search succeeds on retry once the
// (fake) sync's content appears — no separate sync_project call needed.
func TestSearchDependencyDocsHandlerTriggersJITSyncOnMissingGeneration(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	// Deliberately no active generation seeded yet.

	trigger := &fakeSyncTrigger{synced: 1, onSync: func() {
		env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")
	}}

	handler := searchDependencyDocsHandler(env.svc, trigger, true, nil)
	_, out, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !trigger.called {
		t.Fatal("SyncTrigger.SyncProject was never called despite a missing active generation")
	}
	if trigger.calledProjectID != "proj_1" || trigger.calledDependency != "google.golang.org/grpc" {
		t.Errorf("trigger called with projectID=%q dependency=%q, want proj_1/google.golang.org/grpc (scoped to the one missing dependency, not the whole project)", trigger.calledProjectID, trigger.calledDependency)
	}
	if len(out.Chunks) != 1 || out.Chunks[0].Content != "grpc retry docs" {
		t.Errorf("out.Chunks = %+v, want the content the JIT sync produced", out.Chunks)
	}
}

// TestSearchDependencyDocsHandlerSkipsJITSyncWhenDisabled proves
// server.mcp.enable_sync_tool: false is respected exactly as it is for
// sync_project itself — a deliberately read-only session never
// implicitly triggers a sync.
func TestSearchDependencyDocsHandlerSkipsJITSyncWhenDisabled(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	trigger := &fakeSyncTrigger{}

	handler := searchDependencyDocsHandler(env.svc, trigger, false, nil)
	_, _, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err == nil {
		t.Fatal("handler succeeded despite no active generation and no sync, want the original error")
	}
	if !errors.Is(err, query.ErrNoActiveGeneration) {
		t.Errorf("err = %v, want wrapping ErrNoActiveGeneration", err)
	}
	if trigger.called {
		t.Error("SyncTrigger.SyncProject was called despite enableSync=false")
	}
}

// TestSearchDependencyDocsHandlerReportsOriginalErrorWhenJITSyncFails
// proves a failed JIT sync surfaces the original, well-understood
// ErrNoActiveGeneration message rather than a confusing second error
// from a call the agent didn't know was happening.
func TestSearchDependencyDocsHandlerReportsOriginalErrorWhenJITSyncFails(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	trigger := &fakeSyncTrigger{failed: 1}

	handler := searchDependencyDocsHandler(env.svc, trigger, true, nil)
	_, _, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err == nil {
		t.Fatal("handler succeeded despite the JIT sync failing, want the original error")
	}
	if !errors.Is(err, query.ErrNoActiveGeneration) {
		t.Errorf("err = %v, want wrapping ErrNoActiveGeneration (the original error, not a new one from the failed JIT sync)", err)
	}
	if !trigger.called {
		t.Error("SyncTrigger.SyncProject was never called")
	}
}

// TestSearchDependencyDocsHandlerPrefersPriorityBumpOverNewSync is
// WATCH-020's core regression test: when a background sync is already
// running for the project (BumpSyncPriority succeeds), the handler
// waits for the dependency to become active instead of queuing a fully
// redundant second sync request behind the global scheduler lock.
func TestSearchDependencyDocsHandlerPrefersPriorityBumpOverNewSync(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}

	trigger := &fakeSyncTrigger{}
	bumper := &fakePriorityBumper{bump: func(ctx context.Context, projectID, dependency string) (bool, error) {
		// Simulates the already-running background sync reaching this
		// dependency shortly after being bumped.
		env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")
		return true, nil
	}}

	handler := searchDependencyDocsHandler(env.svc, trigger, true, bumper)
	_, out, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !bumper.called || bumper.calledProjectID != "proj_1" || bumper.calledDependency != "google.golang.org/grpc" {
		t.Errorf("bumper called=%v projectID=%q dependency=%q, want true/proj_1/google.golang.org/grpc", bumper.called, bumper.calledProjectID, bumper.calledDependency)
	}
	if trigger.called {
		t.Error("SyncTrigger.SyncProject was called despite BumpSyncPriority succeeding — should have waited instead of queuing a redundant sync")
	}
	if len(out.Chunks) != 1 || out.Chunks[0].Content != "grpc retry docs" {
		t.Errorf("out.Chunks = %+v, want the content the background sync produced", out.Chunks)
	}
}

// TestSearchDependencyDocsHandlerFallsBackWhenNoBumpTarget proves
// WATCH-019's plain JIT-sync path still runs when BumpSyncPriority
// reports no sync is currently running (the common case).
func TestSearchDependencyDocsHandlerFallsBackWhenNoBumpTarget(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}

	trigger := &fakeSyncTrigger{synced: 1, onSync: func() {
		env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")
	}}
	bumper := &fakePriorityBumper{} // bump == nil: always returns false, no sync running

	handler := searchDependencyDocsHandler(env.svc, trigger, true, bumper)
	_, out, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !bumper.called {
		t.Error("BumpSyncPriority was never tried")
	}
	if !trigger.called {
		t.Error("SyncTrigger.SyncProject was never called despite BumpSyncPriority reporting no running sync")
	}
	if len(out.Chunks) != 1 {
		t.Errorf("out.Chunks = %+v, want the JIT sync's content", out.Chunks)
	}
}

// TestSearchDependencyDocsHandlerBumpWaitTimesOutCleanly proves a
// successful bump that never actually produces the dependency (e.g. the
// background sync stalls, or the bump arrived after that dependency's
// action already finished — WATCH-020's own documented failure case)
// times out with the original, well-understood error rather than
// hanging forever.
func TestSearchDependencyDocsHandlerBumpWaitTimesOutCleanly(t *testing.T) {
	prev := jitSyncPriorityWaitBound
	jitSyncPriorityWaitBound = 50 * time.Millisecond
	defer func() { jitSyncPriorityWaitBound = prev }()

	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}

	trigger := &fakeSyncTrigger{}
	bumper := &fakePriorityBumper{bump: func(ctx context.Context, projectID, dependency string) (bool, error) {
		return true, nil // bumped, but nothing ever seeds the generation
	}}

	handler := searchDependencyDocsHandler(env.svc, trigger, true, bumper)
	_, _, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_1", Query: "retry", Dependency: "google.golang.org/grpc"})
	if err == nil {
		t.Fatal("handler succeeded despite the bumped dependency never becoming active, want a timeout error")
	}
	if !errors.Is(err, query.ErrNoActiveGeneration) {
		t.Errorf("err = %v, want wrapping ErrNoActiveGeneration", err)
	}
	if trigger.called {
		t.Error("SyncTrigger.SyncProject was called despite a successful bump — should never fall back once bumped")
	}
}

func TestSearchDependencyDocsHandlerMapsUnknownProjectToActionableError(t *testing.T) {
	env := newTestEnv(t)
	handler := searchDependencyDocsHandler(env.svc, nil, false, nil)
	_, _, err := handler(context.Background(), nil, SearchDependencyDocsIn{ProjectID: "proj_missing", Query: "x", Dependency: "google.golang.org/grpc"})
	if err == nil {
		t.Fatal("handler succeeded, want error")
	}
	if !errors.Is(err, query.ErrProjectNotFound) {
		t.Errorf("err = %v, want wrapping ErrProjectNotFound", err)
	}
}

func TestGetDependencyVersionHandler(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}

	handler := getDependencyVersionHandler(env.svc)
	_, out, err := handler(context.Background(), nil, GetDependencyVersionIn{ProjectID: "proj_1", Package: "google.golang.org/grpc"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.Version != "v1.67.0" {
		t.Errorf("out.Version = %s, want v1.67.0", out.Version)
	}
}

func TestListProjectDependenciesHandler(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{ID: "gen_1"}

	handler := listProjectDependenciesHandler(env.svc)
	_, out, err := handler(context.Background(), nil, ListProjectDependenciesIn{ProjectID: "proj_1"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(out.Dependencies) != 1 || !out.Dependencies[0].HasActiveGeneration {
		t.Errorf("out.Dependencies = %+v, want one with HasActiveGeneration=true", out.Dependencies)
	}
}

func TestKnowledgeStatusHandler(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{ID: "gen_1"}

	handler := knowledgeStatusHandler(env.svc)
	_, out, err := handler(context.Background(), nil, KnowledgeStatusIn{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.TotalProjects != 1 || out.WithActiveGeneration != 1 {
		t.Errorf("out = %+v, unexpected", out)
	}
	if len(out.Projects) != 1 || out.Projects[0].ProjectID != "proj_1" || out.Projects[0].Root != "/repo" {
		t.Errorf("Projects = %+v, want [{proj_1 /repo}] — this is what an agent needs to resolve a real project_id from a directory path", out.Projects)
	}
}

func TestSyncProjectHandlerDisabledByDefault(t *testing.T) {
	trigger := &fakeSyncTrigger{}
	handler := syncProjectHandler(nil, trigger, false)
	_, _, err := handler(context.Background(), nil, SyncProjectIn{ProjectID: "proj_1"})
	if err == nil {
		t.Fatal("handler succeeded, want a disabled-by-config error")
	}
	if trigger.called {
		t.Error("SyncTrigger.SyncProject was called despite the tool being disabled")
	}
}

func TestSyncProjectHandlerEnabledCallsTrigger(t *testing.T) {
	trigger := &fakeSyncTrigger{synced: 2, failed: 0, skipped: 1}
	handler := syncProjectHandler(nil, trigger, true)
	_, out, err := handler(context.Background(), nil, SyncProjectIn{ProjectID: "proj_1"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !trigger.called || trigger.calledProjectID != "proj_1" {
		t.Errorf("trigger.called=%v calledProjectID=%q, want true/proj_1", trigger.called, trigger.calledProjectID)
	}
	if out.Synced != 2 || out.Skipped != 1 {
		t.Errorf("out = %+v, want Synced=2 Skipped=1", out)
	}
}

// TestSyncProjectHandlerClassifiesSentinelErrors is MCP-007: a real
// sync failure (as opposed to the still_running bounded-wait path) must
// be routed through toolError like every other tool, not returned as a
// bare fmt.Errorf-wrapped string — otherwise an agent can never tell a
// query.ErrProjectNotFound-shaped failure apart from an opaque one.
func TestSyncProjectHandlerClassifiesSentinelErrors(t *testing.T) {
	trigger := &fakeSyncTrigger{err: fmt.Errorf("sync: %w", query.ErrProjectNotFound)}
	handler := syncProjectHandler(nil, trigger, true)
	_, _, err := handler(context.Background(), nil, SyncProjectIn{ProjectID: "proj_1"})
	if !errors.Is(err, query.ErrProjectNotFound) {
		t.Fatalf("handler error = %v, want it to still wrap query.ErrProjectNotFound", err)
	}
	if !strings.Contains(err.Error(), "call the scan_project tool first") {
		t.Errorf("handler error = %q, want toolError's actionable project-not-found message", err.Error())
	}
}

func TestScanProjectHandlerDefaultsRootAndCallsTrigger(t *testing.T) {
	trigger := &fakeScanTrigger{ids: []string{"proj_1"}, summary: "new go /repo\n"}
	handler := scanProjectHandler(trigger)
	_, out, err := handler(context.Background(), nil, ScanProjectIn{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !trigger.called || trigger.calledRoot != "." {
		t.Errorf("trigger.called=%v calledRoot=%q, want true/\".\"", trigger.called, trigger.calledRoot)
	}
	if len(out.ProjectIDs) != 1 || out.ProjectIDs[0] != "proj_1" {
		t.Errorf("out.ProjectIDs = %v, want [proj_1]", out.ProjectIDs)
	}
	if out.Note != securityNote {
		t.Errorf("out.Note = %q, want %q", out.Note, securityNote)
	}
}

func TestScanProjectHandlerNilTriggerReportsError(t *testing.T) {
	handler := scanProjectHandler(nil)
	_, _, err := handler(context.Background(), nil, ScanProjectIn{})
	if err == nil {
		t.Fatal("handler succeeded with a nil ScanTrigger, want error")
	}
}

// TestSyncProjectReportsProgressWhenTokenPresent is WATCH-013's
// regression test: a real client/server session (not a bare handler
// call, since the progress token and NotifyProgress delivery only
// exist at the protocol level) proves a client that attaches a
// progress token to its sync_project call receives one notification
// per line the trigger streams, with strictly increasing Progress.
func TestSyncProjectReportsProgressWhenTokenPresent(t *testing.T) {
	trigger := &fakeSyncTrigger{synced: 3, progressLines: []string{"OK a v1", "OK b v2", "OK c v3"}}
	server := New(Deps{Sync: trigger, EnableSyncTool: true})

	var mu sync.Mutex
	var progresses []float64
	var messages []string
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.0"}, &sdkmcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdkmcp.ProgressNotificationClientRequest) {
			mu.Lock()
			defer mu.Unlock()
			progresses = append(progresses, req.Params.Progress)
			messages = append(messages, req.Params.Message)
		},
	})

	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()

	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	params := &sdkmcp.CallToolParams{Name: "sync_project", Arguments: map[string]any{"project_id": "proj_1"}}
	params.SetProgressToken("progress-token-1")
	if _, err := clientSession.CallTool(ctx, params); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(progresses) != 3 {
		t.Fatalf("got %d progress notifications, want 3 (one per streamed line): %v", len(progresses), progresses)
	}
	if progresses[0] != 1 || progresses[1] != 2 || progresses[2] != 3 {
		t.Errorf("progresses = %v, want strictly increasing 1, 2, 3", progresses)
	}
	if messages[0] != "OK a v1" || messages[2] != "OK c v3" {
		t.Errorf("messages = %v, want the trigger's own streamed lines relayed verbatim", messages)
	}
}

// TestSyncProjectSendsNoProgressWithoutToken proves the no-token path
// costs nothing extra: a call with no progress token gets zero
// notifications, even though the trigger still streams lines.
func TestSyncProjectSendsNoProgressWithoutToken(t *testing.T) {
	trigger := &fakeSyncTrigger{synced: 1, progressLines: []string{"OK a v1"}}
	server := New(Deps{Sync: trigger, EnableSyncTool: true})

	notified := false
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.0"}, &sdkmcp.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *sdkmcp.ProgressNotificationClientRequest) { notified = true },
	})

	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()

	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	if _, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "sync_project", Arguments: map[string]any{"project_id": "proj_1"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if notified {
		t.Error("received a progress notification for a call with no progress token")
	}
}

// blockingSyncTrigger blocks until released, for testing
// syncProjectHandler's bounded-wait behavior (WATCH-018): it lets a
// test control exactly when the underlying sync "finishes," independent
// of wall-clock time, and records whether its context was ever
// cancelled.
type blockingSyncTrigger struct {
	release        chan struct{}
	synced         int
	progressLines  []string
	finished       chan struct{} // closed once SyncProject actually returns
	ctxErrAtFinish error
}

func newBlockingSyncTrigger() *blockingSyncTrigger {
	return &blockingSyncTrigger{release: make(chan struct{}), finished: make(chan struct{})}
}

func (f *blockingSyncTrigger) SyncProject(ctx context.Context, projectID string, dependencies []string, progress func(line string)) (int, int, int, error) {
	for _, line := range f.progressLines {
		if progress != nil {
			progress(line)
		}
	}
	<-f.release
	f.ctxErrAtFinish = ctx.Err()
	close(f.finished)
	return f.synced, 0, 0, nil
}

// TestSyncProjectHandlerReturnsPartialWhenBoundExceeded is WATCH-018's
// core regression test: a sync that doesn't finish within
// mcpSyncWaitBound returns a still-running partial response instead of
// blocking the caller indefinitely, and the underlying sync is not
// cancelled by the tool call having already returned.
func TestSyncProjectHandlerReturnsPartialWhenBoundExceeded(t *testing.T) {
	prev := mcpSyncWaitBound
	mcpSyncWaitBound = 20 * time.Millisecond
	defer func() { mcpSyncWaitBound = prev }()

	trigger := newBlockingSyncTrigger()
	trigger.progressLines = []string{"OK a v1", "OK b v2"}
	trigger.synced = 2

	handler := syncProjectHandler(nil, trigger, true)
	_, out, err := handler(context.Background(), nil, SyncProjectIn{ProjectID: "proj_1"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !out.StillRunning {
		t.Fatalf("out = %+v, want StillRunning true", out)
	}
	if out.ReportedSoFar != 2 {
		t.Errorf("out.ReportedSoFar = %d, want 2 (the two progress lines emitted before the bound fired)", out.ReportedSoFar)
	}
	if out.Note != syncStillRunningNote {
		t.Errorf("out.Note = %q, want the still-running note", out.Note)
	}
	if out.Synced != 0 || out.Failed != 0 || out.Skipped != 0 {
		t.Errorf("out = %+v, want the final tally fields left zero — they're not known yet", out)
	}

	// Release the trigger now and confirm the background sync still
	// completes cleanly, with an undamaged (non-cancelled) context —
	// the handler having already returned must not have killed it.
	close(trigger.release)
	select {
	case <-trigger.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("background sync never finished after being released")
	}
	if trigger.ctxErrAtFinish != nil {
		t.Errorf("background sync's context was cancelled (%v), want it detached from the timed-out handler call", trigger.ctxErrAtFinish)
	}
}

// TestServerEndToEndOverInMemoryTransport is MCP-003's own integration
// test: a real server (with a real query.Service backed by fakes) and a
// real client, connected via the SDK's in-memory transport pair, with
// an actual tool-call round trip.
func TestServerEndToEndOverInMemoryTransport(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")

	server := New(Deps{Query: env.svc})

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

	res, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "search_dependency_docs",
		Arguments: map[string]any{
			"project_id": "proj_1",
			"query":      "retry",
			"dependency": "google.golang.org/grpc",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned an error result: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("CallTool result has no content")
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("res.Content[0] = %T, want *sdkmcp.TextContent", res.Content[0])
	}
	if text.Text == "" {
		t.Error("result text content is empty")
	}
}

// TestSyncProgressNoteDistinguishesNeverRanFromRanWithNothingToDo is the
// MCP-side half of the same regression internal/daemon's
// sync_progress_stale_test.go covers for the Scheduler: a real MCP
// session (opencode, live) read "no sync has run for this project yet"
// for a project whose sync had genuinely just run and correctly found
// nothing new to build, and concluded the sync tool must be disabled.
// The Ran field is what the note must key off to avoid repeating that.
func TestSyncProgressNoteDistinguishesNeverRanFromRanWithNothingToDo(t *testing.T) {
	never := syncProgressNote(SyncProgressOut{Ran: false, Total: 0})
	if !strings.Contains(never, "no sync has run for this project yet") {
		t.Errorf("never-ran note = %q, want it to say no sync has run yet", never)
	}

	nothingToDo := syncProgressNote(SyncProgressOut{Ran: true, Total: 0})
	if strings.Contains(nothingToDo, "no sync has run for this project yet") {
		t.Errorf("ran-but-nothing-to-do note = %q, must not claim no sync has run", nothingToDo)
	}
	if !strings.Contains(nothingToDo, "nothing new to sync") {
		t.Errorf("ran-but-nothing-to-do note = %q, want it to explain a sync did run and found nothing new", nothingToDo)
	}

	lastRun := syncProgressNote(SyncProgressOut{Ran: true, Total: 5, Done: 5, Failed: 1})
	if !strings.Contains(lastRun, "5 of 5") {
		t.Errorf("last-run note = %q, want the real counters", lastRun)
	}

	syncing := syncProgressNote(SyncProgressOut{Syncing: true, Ran: true, Total: 5, Done: 2})
	if !strings.Contains(syncing, "a sync is running") {
		t.Errorf("syncing note = %q, want the in-progress message", syncing)
	}
}

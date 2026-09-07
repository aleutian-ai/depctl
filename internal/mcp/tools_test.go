package mcp

import (
	"context"
	"errors"
	"testing"

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
	projects    map[string]domain.Project
	resolutions map[string]domain.Resolution
	active      map[string]domain.Generation
	refs        []domain.VersionReference
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
	calledProjectID         string
}

func (f *fakeSyncTrigger) SyncProject(ctx context.Context, projectID string) (int, int, int, error) {
	f.called = true
	f.calledProjectID = projectID
	return f.synced, f.failed, f.skipped, f.err
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
	e.control.active[string(ecosystem)+"|"+pkg+"|qdrant"] = domain.Generation{ID: generationID}
}

func TestSearchDependencyDocsHandlerReturnsChunksWithSecurityNote(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc retry docs")

	handler := searchDependencyDocsHandler(env.svc)
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

func TestSearchDependencyDocsHandlerMapsUnknownProjectToActionableError(t *testing.T) {
	env := newTestEnv(t)
	handler := searchDependencyDocsHandler(env.svc)
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
	handler := syncProjectHandler(trigger, false)
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
	handler := syncProjectHandler(trigger, true)
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

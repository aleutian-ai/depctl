package query

import (
	"context"
	"errors"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/domain"
)

// fakeEmbedder returns a fixed vector regardless of text, so tests can
// assert on backend behavior without needing real embeddings.
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

// fakeControlStore is a minimal in-memory ControlStore.
type fakeControlStore struct {
	projects    map[string]domain.Project
	resolutions map[string]domain.Resolution
	active      map[string]domain.Generation // key: eco|pkg|backendName
	refs        []domain.VersionReference
	generations []domain.Generation // every generation ever seeded, any state — see seedChunk
}

func newFakeControlStore() *fakeControlStore {
	return &fakeControlStore{
		projects:    map[string]domain.Project{},
		resolutions: map[string]domain.Resolution{},
		active:      map[string]domain.Generation{},
	}
}

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

func (s *fakeControlStore) ListGenerationsByDependencyVersion(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.Generation, error) {
	var out []domain.Generation
	for _, g := range s.generations {
		if g.Dependency.Dependency.Ecosystem == ecosystem && g.Dependency.Dependency.Name == pkg && g.Dependency.Version == version {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *fakeControlStore) ListAllReferences(ctx context.Context) ([]domain.VersionReference, error) {
	return s.refs, nil
}

var errNotFound = errors.New("not found")

// fakeDataStore is a minimal in-memory DataStore.
type fakeDataStore struct {
	chunks  map[string]domain.Chunk // key: generationID|chunkID
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
	var out []domain.Chunk
	prefix := generationID + "|"
	for key, c := range s.chunks {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			out = append(out, c)
		}
	}
	return out, nil
}

// testEnv bundles a fully wired fake Service plus its stores, and a
// helper to seed one indexed chunk for a given dependency+version.
type testEnv struct {
	control *fakeControlStore
	data    *fakeDataStore
	vb      *backendtest.Backend
	ns      backend.Namespace
	svc     *Service
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
	svc := New(control, data, vb, &fakeEmbedder{dims: 4}, ns, "qdrant")
	return &testEnv{control: control, data: data, vb: vb, ns: ns, svc: svc}
}

// seedChunk indexes one chunk for (ecosystem, pkg, version, generation)
// into both the fake backend and fake Badger, and marks that generation
// active for the backend name the test env uses.
func (e *testEnv) seedChunk(t *testing.T, ecosystem domain.Ecosystem, pkg, version, generationID, chunkID, content string) {
	t.Helper()
	ctx := context.Background()
	err := e.vb.Upsert(ctx, backend.UpsertRequest{
		Namespace: e.ns.Name,
		Points: []backend.Point{{
			ID:     chunkID,
			Vector: []float32{1, 0, 0, 0},
			Metadata: backend.PointMetadata{
				Ecosystem: string(ecosystem), Dependency: pkg, Version: version, Generation: generationID, SourceType: "git", Authority: 100,
			},
		}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	e.data.chunks[generationID+"|"+chunkID] = domain.Chunk{ID: chunkID, ObjectID: "ko_1", Content: []byte(content)}
	gen := domain.Generation{
		ID:         generationID,
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: ecosystem, Name: pkg}, Version: version},
		State:      domain.GenActive,
	}
	// The single active-pointer fixture entry (one per eco|pkg|backend —
	// a second seedChunk call for a different version of the same
	// dependency overwrites this, matching real active_generations
	// semantics: only one generation is ever "the" active pointer).
	e.control.active[string(ecosystem)+"|"+pkg+"|qdrant"] = gen
	// The per-version promoted-generation history (every seedChunk call
	// appends, never overwritten) — searchProject's real, correctly-
	// scoped check (VALID-001) is "was THIS version ever promoted,"
	// independent of whether it's still the current active pointer; see
	// searchProject's own comment for why checking the active pointer's
	// Version directly was tried and reverted (it broke VALID-002's
	// multi-project scenario).
	e.control.generations = append(e.control.generations, gen)
}

func testDep(pkg, version string) domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: pkg},
		Version:    version,
	}
}

func TestSearchProjectModeResolvesActiveVersionFilter(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{testDep("google.golang.org/grpc", "v1.67.0")}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "grpc docs content")
	// A different version's chunk must never be returned for a project search.
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.60.0", "gen_0", "chk_0", "old grpc docs")
	// re-assert current version is active (seedChunk's second call above
	// overwrote the single active-pointer fixture entry) — Version must
	// be set here too, matching what a real promote.Promote always
	// populates.
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{
		ID:         "gen_1",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}

	result, err := env.svc.SearchKnowledge(context.Background(), Query{ProjectID: "proj_1", Text: "retry", Dependency: "google.golang.org/grpc", Mode: ModeProject})
	if err != nil {
		t.Fatalf("SearchKnowledge: %v", err)
	}
	if len(result.Chunks) != 1 {
		t.Fatalf("Chunks = %+v, want exactly 1 (version-filtered)", result.Chunks)
	}
	if result.Chunks[0].Version != "v1.67.0" || result.Chunks[0].Content != "grpc docs content" {
		t.Errorf("Chunks[0] = %+v, want v1.67.0's content", result.Chunks[0])
	}
	if result.Chunks[0].TrustClass != domain.TrustRepository {
		t.Errorf("Chunks[0].TrustClass = %q, want %q (derived from SourceType %q)", result.Chunks[0].TrustClass, domain.TrustRepository, result.Chunks[0].SourceType)
	}
}

func TestSearchProjectModeUnknownProjectReturnsTypedError(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.svc.SearchKnowledge(context.Background(), Query{ProjectID: "proj_missing", Text: "x", Dependency: "google.golang.org/grpc", Mode: ModeProject})
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestSearchProjectModeUnknownDependencyReturnsTypedError(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{testDep("google.golang.org/grpc", "v1.67.0")}}

	_, err := env.svc.SearchKnowledge(context.Background(), Query{ProjectID: "proj_1", Text: "x", Dependency: "github.com/pkg/errors", Mode: ModeProject})
	if !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("err = %v, want ErrDependencyNotFound", err)
	}
}

func TestSearchProjectModeNoActiveGenerationReturnsTypedError(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{testDep("google.golang.org/grpc", "v1.67.0")}}
	// No active generation seeded — dependency resolves, but has never
	// been synced.

	_, err := env.svc.SearchKnowledge(context.Background(), Query{ProjectID: "proj_1", Text: "x", Dependency: "google.golang.org/grpc", Mode: ModeProject})
	if !errors.Is(err, ErrNoActiveGeneration) {
		t.Errorf("err = %v, want ErrNoActiveGeneration", err)
	}
}

// TestSearchProjectModeVersionMismatchReturnsTypedErrorNotEmptyResult is
// the regression test for a bug VALID-001's live testing found: the
// project resolves a version with NO promoted generation of its own
// (some *other* version of the same dependency is what's actually
// active) — searchProject's old GetActiveGeneration check only verified
// "does a generation exist for this ecosystem+package at all," not that
// it matches the project's resolved version, so this case silently fell
// through to an empty-but-successful search result (safe — never
// cross-version content — but confusingly empty instead of an
// actionable error, and WATCH-019's JIT-sync trigger, which
// pattern-matches ErrNoActiveGeneration, never fired for it).
func TestSearchProjectModeVersionMismatchReturnsTypedErrorNotEmptyResult(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	// The project resolves v1.68.0, but only v1.67.0 has ever been
	// synced/promoted — a real scenario (e.g. a dependency upgrade whose
	// sync hasn't completed yet).
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{testDep("google.golang.org/grpc", "v1.68.0")}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "old docs")

	_, err := env.svc.SearchKnowledge(context.Background(), Query{ProjectID: "proj_1", Text: "x", Dependency: "google.golang.org/grpc", Mode: ModeProject})
	if !errors.Is(err, ErrNoActiveGeneration) {
		t.Errorf("err = %v, want ErrNoActiveGeneration — not a silent empty result", err)
	}
}

func TestSearchCompareModeLabelsEachResultWithItsVersion(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{testDep("google.golang.org/grpc", "v1.67.0")}}
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "gen_1", "chk_1", "current docs")
	env.control.refs = append(env.control.refs, domain.VersionReference{
		Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.68.0", Reason: domain.ReferenceReasonLatest,
	})
	env.seedChunk(t, domain.EcosystemGo, "google.golang.org/grpc", "v1.68.0", "gen_2", "chk_2", "latest docs")
	// seedChunk's second call above overwrote the single active-pointer
	// fixture entry to gen_2/v1.68.0 — but the project's own resolved
	// (active) version is v1.67.0/gen_1; v1.68.0 is reachable only via
	// the "latest" reference, not the active pointer. Re-assert it.
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{
		ID:         "gen_1",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}, Version: "v1.67.0"},
	}

	result, err := env.svc.SearchKnowledge(context.Background(), Query{ProjectID: "proj_1", Text: "retry", Dependency: "google.golang.org/grpc", Mode: ModeCompare})
	if err != nil {
		t.Fatalf("SearchKnowledge: %v", err)
	}
	if len(result.Chunks) != 2 {
		t.Fatalf("Chunks = %+v, want 2 (one per version)", result.Chunks)
	}
	versions := map[string]bool{}
	for _, c := range result.Chunks {
		versions[c.Version] = true
	}
	if !versions["v1.67.0"] || !versions["v1.68.0"] {
		t.Errorf("versions found = %v, want both v1.67.0 and v1.68.0", versions)
	}
}

func TestGetProjectDependenciesFlagsActiveGeneration(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		testDep("google.golang.org/grpc", "v1.67.0"),
		testDep("github.com/pkg/errors", "v0.9.1"),
	}}
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{ID: "gen_1"}

	deps, err := env.svc.GetProjectDependencies(context.Background(), "proj_1")
	if err != nil {
		t.Fatalf("GetProjectDependencies: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("deps = %+v, want 2", deps)
	}
	for _, d := range deps {
		want := d.Dependency.Dependency.Name == "google.golang.org/grpc"
		if d.HasActiveGeneration != want {
			t.Errorf("%s HasActiveGeneration = %v, want %v", d.Dependency.Dependency.Name, d.HasActiveGeneration, want)
		}
	}
}

func TestGetDependencyVersionUnknownDependency(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{testDep("google.golang.org/grpc", "v1.67.0")}}

	_, err := env.svc.GetDependencyVersion(context.Background(), "proj_1", "does-not-exist")
	if !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("err = %v, want ErrDependencyNotFound", err)
	}
}

func TestGetProvenanceResolvesThroughToSourceObject(t *testing.T) {
	env := newTestEnv(t)
	env.data.chunks["gen_1|chk_1"] = domain.Chunk{ID: "chk_1", ObjectID: "ko_1", Content: []byte("hello")}
	env.data.objects["ko_1"] = domain.KnowledgeObject{
		ID: "ko_1", SourceURI: "https://github.com/grpc/grpc-go", SourceType: "git", LogicalPath: "README.md", Authority: 100,
		Dependency: testDep("google.golang.org/grpc", "v1.67.0"),
	}

	prov, err := env.svc.GetProvenance(context.Background(), "gen_1", "chk_1")
	if err != nil {
		t.Fatalf("GetProvenance: %v", err)
	}
	if prov.SourceURI != "https://github.com/grpc/grpc-go" || prov.LogicalPath != "README.md" || prov.Version != "v1.67.0" {
		t.Errorf("Provenance = %+v, unexpected fields", prov)
	}
}

func TestGetReleaseChangesReturnsExcerptsForFromAndToOnly(t *testing.T) {
	env := newTestEnv(t)
	env.control.refs = append(env.control.refs, domain.VersionReference{
		Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject,
	})
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{ID: "gen_1"}
	env.data.chunks["gen_1|chk_v160"] = domain.Chunk{ID: "chk_v160", Content: []byte("v1.60.0 changes"), Metadata: map[string]string{"content_type": "release_note", "release_version": "v1.60.0"}}
	env.data.chunks["gen_1|chk_v165"] = domain.Chunk{ID: "chk_v165", Content: []byte("v1.65.0 changes"), Metadata: map[string]string{"content_type": "release_note", "release_version": "v1.65.0"}}
	env.data.chunks["gen_1|chk_v167"] = domain.Chunk{ID: "chk_v167", Content: []byte("v1.67.0 changes"), Metadata: map[string]string{"content_type": "release_note", "release_version": "v1.67.0"}}
	env.data.chunks["gen_1|chk_readme"] = domain.Chunk{ID: "chk_readme", Content: []byte("not a release note"), Metadata: map[string]string{"content_type": "markdown"}}

	changes, err := env.svc.GetReleaseChanges(context.Background(), "google.golang.org/grpc", "v1.60.0", "v1.67.0")
	if err != nil {
		t.Fatalf("GetReleaseChanges: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want exactly 2 (v1.60.0 and v1.67.0, not v1.65.0 or the readme)", changes)
	}
	versions := map[string]bool{}
	for _, c := range changes {
		versions[c.Version] = true
	}
	if !versions["v1.60.0"] || !versions["v1.67.0"] {
		t.Errorf("versions = %v, want v1.60.0 and v1.67.0", versions)
	}
}

func TestGetReleaseChangesUnknownDependency(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.svc.GetReleaseChanges(context.Background(), "does-not-exist", "v1.0.0", "v2.0.0")
	if !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("err = %v, want ErrDependencyNotFound", err)
	}
}

func TestStatusTalliesActiveAndPendingGenerations(t *testing.T) {
	env := newTestEnv(t)
	env.control.projects["proj_1"] = domain.Project{ID: "proj_1", Root: "/repo1"}
	env.control.resolutions["proj_1"] = domain.Resolution{Dependencies: []domain.DependencyVersion{
		testDep("google.golang.org/grpc", "v1.67.0"),
		testDep("github.com/pkg/errors", "v0.9.1"),
	}}
	env.control.active["go|google.golang.org/grpc|qdrant"] = domain.Generation{ID: "gen_1"}
	env.control.projects["proj_2"] = domain.Project{ID: "proj_2", Root: "/repo2"}
	// proj_2 registered but never scanned/resolved — must be skipped,
	// not error the whole tally.

	status, err := env.svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.TotalProjects != 2 {
		t.Errorf("TotalProjects = %d, want 2", status.TotalProjects)
	}
	if status.TotalDependencies != 2 {
		t.Errorf("TotalDependencies = %d, want 2 (only proj_1's, proj_2 unresolved)", status.TotalDependencies)
	}
	if status.WithActiveGeneration != 1 || status.WithoutActiveGeneration != 1 {
		t.Errorf("WithActiveGeneration=%d WithoutActiveGeneration=%d, want 1 and 1", status.WithActiveGeneration, status.WithoutActiveGeneration)
	}

	byID := map[string]string{}
	for _, p := range status.Projects {
		byID[p.ID] = p.Root
	}
	if byID["proj_1"] != "/repo1" || byID["proj_2"] != "/repo2" {
		t.Errorf("Projects = %+v, want proj_1->/repo1 and proj_2->/repo2 (both listed even though proj_2 is unresolved)", status.Projects)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/api"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
)

// seedExportFixture seeds a project with one resolved, actively-generated
// dependency carrying chunkCount real chunks (and their parent knowledge
// object), so engine.ExportMem0 has real content to read and push.
func seedExportFixture(t *testing.T, store *bboltstore.Store, badgerStore *badgerstore.Store, projectID string, eco domain.Ecosystem, pkg, version string, chunkCount int) string {
	t.Helper()
	ctx := context.Background()

	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: projectID, Ecosystem: eco, Package: pkg, Version: version, Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	resolution := domain.Resolution{
		Ecosystem: eco,
		Dependencies: []domain.DependencyVersion{
			{Dependency: domain.Dependency{Ecosystem: eco, Name: pkg, Direct: true}, Version: version},
		},
	}
	if err := store.PutResolution(ctx, projectID, resolution); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	genID := seedActiveGeneration(t, store, badgerStore, eco, pkg, version, chunkCount)

	dep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: eco, Name: pkg}, Version: version}
	obj := domain.KnowledgeObject{
		ID:         "obj_" + genID,
		Dependency: dep,
		SourceType: "git",
		TrustClass: domain.TrustRepository,
		Authority:  100,
		Content:    []byte("object content"),
	}
	if err := badgerStore.PutKnowledgeObject(ctx, obj); err != nil {
		t.Fatalf("PutKnowledgeObject: %v", err)
	}
	for i := 0; i < chunkCount; i++ {
		chunk := domain.Chunk{
			ID:       obj.ID + "_chunk_" + string(rune('a'+i)),
			ObjectID: obj.ID,
			Ordinal:  i,
			Content:  []byte("chunk content " + string(rune('a'+i))),
		}
		if err := badgerStore.PutChunk(ctx, genID, chunk); err != nil {
			t.Fatalf("PutChunk: %v", err)
		}
	}
	return genID
}

// exportTestEngine builds a real engine (not a fake) over real temp
// stores, so engine.ExportMem0 exercises its actual resolution-walk and
// badger reads, not a stand-in.
func exportTestEngine(t *testing.T, cfg config.Config) (*engine, *bboltstore.Store, *badgerstore.Store) {
	t.Helper()
	store, badgerStore := describeTestStores(t)
	e, err := newEngine(store, badgerStore, cfg, "unused-control-path", "unused-badger-path", nil, nil)
	if err != nil {
		t.Fatalf("newEngine: %v", err)
	}
	return e, store, badgerStore
}

func TestExportMem0PushesEveryChunkWithCorrectMetadata(t *testing.T) {
	var gotRequests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/entities/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotRequests = append(gotRequests, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Config{Export: config.ExportConfig{Mem0: config.Mem0ExportConfig{Endpoint: srv.URL}}, Vector: config.VectorConfig{Backend: "qdrant"}}
	e, store, badgerStore := exportTestEngine(t, cfg)
	genID := seedExportFixture(t, store, badgerStore, "proj_1", domain.EcosystemGo, "google.golang.org/protobuf", "v1.36.11", 2)

	var out testWriter
	resp, err := e.ExportMem0(context.Background(), api.ExportMem0Request{ProjectID: "proj_1"}, &out)
	if err != nil {
		t.Fatalf("ExportMem0: %v", err)
	}

	if len(resp.Results) != 1 || resp.Results[0].Pushed != 2 || resp.Results[0].Failed != 0 {
		t.Fatalf("Results = %+v, want one dependency with Pushed=2, Failed=0", resp.Results)
	}
	if len(gotRequests) != 2 {
		t.Fatalf("got %d AddMemory requests, want 2", len(gotRequests))
	}
	meta, ok := gotRequests[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("request metadata = %v, want a map", gotRequests[0]["metadata"])
	}
	if meta["ecosystem"] != "go" || meta["dependency"] != "google.golang.org/protobuf" || meta["version"] != "v1.36.11" || meta["generation"] != genID || meta["trust_class"] != "repository" {
		t.Errorf("metadata = %+v, got mismatched fields (generation want %s)", meta, genID)
	}
	if gotRequests[0]["user_id"] != "proj_1" {
		t.Errorf("user_id = %v, want proj_1", gotRequests[0]["user_id"])
	}
}

func TestExportMem0PartialFailureDoesNotAbortBatch(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/entities/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Config{Export: config.ExportConfig{Mem0: config.Mem0ExportConfig{Endpoint: srv.URL}}, Vector: config.VectorConfig{Backend: "qdrant"}}
	e, store, badgerStore := exportTestEngine(t, cfg)
	seedExportFixture(t, store, badgerStore, "proj_1", domain.EcosystemGo, "google.golang.org/protobuf", "v1.36.11", 2)

	var out testWriter
	resp, err := e.ExportMem0(context.Background(), api.ExportMem0Request{ProjectID: "proj_1"}, &out)
	if err != nil {
		t.Fatalf("ExportMem0: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Pushed != 1 || resp.Results[0].Failed != 1 {
		t.Fatalf("Results = %+v, want one dependency with Pushed=1, Failed=1", resp.Results)
	}
}

func TestExportMem0RequiresEndpoint(t *testing.T) {
	e, store, badgerStore := exportTestEngine(t, config.Config{Vector: config.VectorConfig{Backend: "qdrant"}})
	seedExportFixture(t, store, badgerStore, "proj_1", domain.EcosystemGo, "google.golang.org/protobuf", "v1.36.11", 1)

	var out testWriter
	_, err := e.ExportMem0(context.Background(), api.ExportMem0Request{ProjectID: "proj_1"}, &out)
	if err == nil {
		t.Fatal("expected an error when no mem0 endpoint is configured, got nil")
	}
}

// testWriter is a minimal io.Writer so tests don't need to pull in
// bytes.Buffer just to satisfy the out parameter.
type testWriter struct{ data []byte }

func (w *testWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

func TestExportGraphitiPushesOneEpisodePerDependency(t *testing.T) {
	var gotRequests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // Health's GET /healthcheck
			w.WriteHeader(http.StatusOK)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotRequests = append(gotRequests, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Config{Export: config.ExportConfig{Graphiti: config.GraphitiExportConfig{Endpoint: srv.URL}}, Vector: config.VectorConfig{Backend: "qdrant"}}
	e, store, badgerStore := exportTestEngine(t, cfg)
	seedExportFixture(t, store, badgerStore, "proj_1", domain.EcosystemGo, "google.golang.org/protobuf", "v1.36.11", 2)

	var out testWriter
	resp, err := e.ExportGraphiti(context.Background(), api.ExportGraphitiRequest{ProjectID: "proj_1"}, &out)
	if err != nil {
		t.Fatalf("ExportGraphiti: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Pushed != 1 || resp.Results[0].Failed != 0 {
		t.Fatalf("Results = %+v, want one dependency, Pushed=1 (one episode)", resp.Results)
	}
	if len(gotRequests) != 1 {
		t.Fatalf("got %d POST /messages requests, want 1 (one episode for the whole dependency)", len(gotRequests))
	}
	if gotRequests[0]["group_id"] != "proj_1" {
		t.Errorf("group_id = %v, want proj_1", gotRequests[0]["group_id"])
	}
	messages, _ := gotRequests[0]["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v, want exactly 1", messages)
	}
	msg := messages[0].(map[string]any)
	var content map[string]any
	if err := json.Unmarshal([]byte(msg["content"].(string)), &content); err != nil {
		t.Fatalf("episode content is not valid JSON: %v", err)
	}
	chunks, _ := content["chunks"].([]any)
	if len(chunks) != 2 {
		t.Errorf("episode chunks = %d, want 2 (both chunks bundled into one episode)", len(chunks))
	}
}

func TestExportCogneeAddsThenCognifies(t *testing.T) {
	var addCalls, cognifyCalls int
	var cognifyDatasets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v1/add":
			addCalls++
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v1/cognify":
			cognifyCalls++
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if datasets, ok := body["datasets"].([]any); ok {
				for _, d := range datasets {
					cognifyDatasets = append(cognifyDatasets, d.(string))
				}
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := config.Config{Export: config.ExportConfig{Cognee: config.CogneeExportConfig{Endpoint: srv.URL}}, Vector: config.VectorConfig{Backend: "qdrant"}}
	e, store, badgerStore := exportTestEngine(t, cfg)
	seedExportFixture(t, store, badgerStore, "proj_1", domain.EcosystemGo, "google.golang.org/protobuf", "v1.36.11", 2)

	var out testWriter
	resp, err := e.ExportCognee(context.Background(), api.ExportCogneeRequest{ProjectID: "proj_1"}, &out)
	if err != nil {
		t.Fatalf("ExportCognee: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Pushed != 1 {
		t.Fatalf("Results = %+v, want one dependency with Pushed=1 (one file added)", resp.Results)
	}
	if resp.CognifyError != "" {
		t.Errorf("CognifyError = %q, want empty", resp.CognifyError)
	}
	if addCalls != 1 {
		t.Errorf("addCalls = %d, want 1", addCalls)
	}
	if cognifyCalls != 1 {
		t.Errorf("cognifyCalls = %d, want 1 (once for the whole dataset, not per dependency)", cognifyCalls)
	}
	if len(cognifyDatasets) != 1 || cognifyDatasets[0] != "proj_1" {
		t.Errorf("cognify datasets = %v, want [proj_1]", cognifyDatasets)
	}
}

func TestExportCogneeSkipsCognifyWhenNothingWasAdded(t *testing.T) {
	var cognifyCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/api/v1/cognify" {
			cognifyCalls++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.Config{Export: config.ExportConfig{Cognee: config.CogneeExportConfig{Endpoint: srv.URL}}, Vector: config.VectorConfig{Backend: "qdrant"}}
	e, store, _ := exportTestEngine(t, cfg)
	// No fixture seeded — the project has no resolution at all.
	if err := store.PutResolution(context.Background(), "proj_1", domain.Resolution{}); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	var out testWriter
	resp, err := e.ExportCognee(context.Background(), api.ExportCogneeRequest{ProjectID: "proj_1"}, &out)
	if err != nil {
		t.Fatalf("ExportCognee: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("Results = %+v, want none", resp.Results)
	}
	if cognifyCalls != 0 {
		t.Errorf("cognifyCalls = %d, want 0 — nothing was added, so cognify must not run", cognifyCalls)
	}
}

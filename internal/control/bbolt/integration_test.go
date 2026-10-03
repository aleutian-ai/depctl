package bbolt_test

// Cross-store restart integration test (STORE-004): proves bbolt
// (control plane) and Badger (data plane) remain mutually usable after
// both are closed and reopened — the invariant neither store's own
// restart tests can prove on their own, since ragctl deliberately splits
// control state and content across two separate databases and a caller
// (query.Service, generation.Build/Replicate) always needs both to agree.
//
// Deliberately package bbolt_test (not bbolt), and deliberately no new
// production code beyond STORE-004's own scope — per the ticket's
// explicit warning against introducing a unifying storage facade before
// a second real caller needs one.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
)

func TestStorageRestartPersistence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control.db")
	dataPath := filepath.Join(dir, "badger")

	// --- first process lifetime: write, promote, close ---
	control, err := bboltstore.Open(controlPath)
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	data, err := badgerstore.Open(dataPath)
	if err != nil {
		t.Fatalf("open data store: %v", err)
	}

	proj := domain.Project{
		ID:        "proj_storage_boundary",
		Root:      "/tmp/storage-boundary-fixture",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := control.PutProject(ctx, proj); err != nil {
		t.Fatalf("PutProject: %v", err)
	}

	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/storagefixture", Direct: true},
		Version:    "v1.0.0",
		ResolvedBy: "go-list",
	}
	gen := domain.Generation{
		ID:         "gen_storage_boundary_01",
		Dependency: dep,
		State:      domain.GenPlanned,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := control.PutGeneration(ctx, gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}

	obj := domain.KnowledgeObject{
		ID:          "ko_storage_boundary_01",
		Dependency:  dep,
		SourceID:    "fixture",
		SourceURI:   "https://example.com/storagefixture",
		SourceType:  "git",
		TrustClass:  domain.TrustRepository,
		LogicalPath: "README.md",
		Title:       "Storage Boundary Fixture",
		Version:     dep.Version,
		Content:     []byte("fixture content for the storage integration boundary test"),
		ContentHash: "fixturehash",
	}
	if err := data.PutKnowledgeObject(ctx, obj); err != nil {
		t.Fatalf("PutKnowledgeObject: %v", err)
	}

	chunk := domain.Chunk{
		ID:          "chk_storage_boundary_01",
		ObjectID:    obj.ID,
		Ordinal:     0,
		Content:     obj.Content,
		ContentHash: obj.ContentHash,
	}
	if err := data.PutChunk(ctx, gen.ID, chunk); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	manifest, err := json.Marshal(map[string]any{
		"generation_id": gen.ID,
		"object_count":  1,
		"chunk_count":   1,
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := data.PutManifest(ctx, gen.ID, manifest); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	if err := control.PromoteGeneration(ctx, gen, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}

	if err := control.Close(); err != nil {
		t.Fatalf("close control store: %v", err)
	}
	if err := data.Close(); err != nil {
		t.Fatalf("close data store: %v", err)
	}

	// --- second process lifetime: reopen, verify everything survived ---
	control2, err := bboltstore.Open(controlPath)
	if err != nil {
		t.Fatalf("reopen control store: %v", err)
	}
	defer func() { _ = control2.Close() }()
	data2, err := badgerstore.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen data store: %v", err)
	}
	defer func() { _ = data2.Close() }()

	gotProj, err := control2.GetProject(ctx, proj.ID)
	if err != nil {
		t.Fatalf("GetProject after restart: %v", err)
	}
	if gotProj.Root != proj.Root {
		t.Fatalf("project.Root after restart = %q, want %q", gotProj.Root, proj.Root)
	}

	activeGen, err := control2.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version, "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration after restart: %v", err)
	}
	if activeGen.ID != gen.ID {
		t.Fatalf("active generation ID after restart = %q, want %q", activeGen.ID, gen.ID)
	}
	if activeGen.State != domain.GenActive {
		t.Fatalf("active generation state after restart = %q, want %q", activeGen.State, domain.GenActive)
	}

	// The cross-store step this test exists for: resolve the active
	// generation from bbolt, then use its ID to read Badger content —
	// proving the two databases still agree with each other, not just
	// that each independently persisted its own data.
	gotChunks, err := data2.ListGenerationChunks(ctx, activeGen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks after restart: %v", err)
	}
	if len(gotChunks) != 1 || string(gotChunks[0].Content) != string(chunk.Content) {
		t.Fatalf("chunks after restart = %+v, want one chunk with content %q", gotChunks, chunk.Content)
	}

	gotObj, err := data2.GetKnowledgeObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetKnowledgeObject after restart: %v", err)
	}
	if string(gotObj.Content) != string(obj.Content) {
		t.Fatalf("object content after restart = %q, want %q", gotObj.Content, obj.Content)
	}

	gotManifest, err := data2.GetManifest(ctx, activeGen.ID)
	if err != nil {
		t.Fatalf("GetManifest after restart: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotManifest, &decoded); err != nil {
		t.Fatalf("unmarshal manifest after restart: %v", err)
	}
	if decoded["generation_id"] != gen.ID {
		t.Fatalf("manifest generation_id after restart = %v, want %q", decoded["generation_id"], gen.ID)
	}
}

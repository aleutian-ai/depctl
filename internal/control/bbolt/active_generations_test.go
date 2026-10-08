package bbolt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const testDep = "google.golang.org/grpc"

func testGeneration(id, version string, state domain.GenerationState) domain.Generation {
	return domain.Generation{
		ID: id,
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: testDep},
			Version:    version,
		},
		State: state,
	}
}

func putAndPromote(t *testing.T, store *Store, gen domain.Generation, backend string) {
	t.Helper()
	ctx := context.Background()
	if err := store.PutGeneration(ctx, gen); err != nil {
		t.Fatalf("PutGeneration %s: %v", gen.ID, err)
	}
	if err := store.PromoteGeneration(ctx, gen, backend); err != nil {
		t.Fatalf("PromoteGeneration %s: %v", gen.ID, err)
	}
}

func mustState(t *testing.T, store *Store, id string, want domain.GenerationState) {
	t.Helper()
	got, err := store.GetGeneration(context.Background(), id)
	if err != nil {
		t.Fatalf("GetGeneration %s: %v", id, err)
	}
	if got.State != want {
		t.Errorf("%s state = %s, want %s", id, got.State, want)
	}
}

func mustActive(t *testing.T, store *Store, version, wantID string) {
	t.Helper()
	got, err := store.GetActiveGeneration(context.Background(), domain.EcosystemGo, testDep, version, "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration %s: %v", version, err)
	}
	if got.ID != wantID {
		t.Errorf("active for %s = %s, want %s", version, got.ID, wantID)
	}
}

func TestGetActiveGenerationNotFound(t *testing.T) {
	store := openTestStore(t)
	_, err := store.GetActiveGeneration(context.Background(), domain.EcosystemGo, testDep, "v1.67.0", "qdrant")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveGeneration = %v, want ErrNotFound", err)
	}
}

func TestPromoteGenerationWithNoPriorJustActivates(t *testing.T) {
	store := openTestStore(t)
	putAndPromote(t, store, testGeneration("gen_1", "v1.67.0", domain.GenReady), "qdrant")
	mustState(t, store, "gen_1", domain.GenActive)
	mustActive(t, store, "v1.67.0", "gen_1")
}

// TestPromoteRebuildSupersedesSameVersionOnly: promoting a second
// generation of the same version is a rebuild and supersedes the first.
func TestPromoteRebuildSupersedesSameVersionOnly(t *testing.T) {
	store := openTestStore(t)
	putAndPromote(t, store, testGeneration("gen_old", "v1.67.0", domain.GenReady), "qdrant")
	putAndPromote(t, store, testGeneration("gen_new", "v1.67.0", domain.GenReady), "qdrant")

	mustState(t, store, "gen_old", domain.GenSuperseded)
	mustState(t, store, "gen_new", domain.GenActive)
	mustActive(t, store, "v1.67.0", "gen_new")
}

// TestDifferentVersionsStayActiveTogether is ADR-012's invariant: two
// projects on different versions of one dependency are both served.
// Before ADR-012, promoting v1.67.0 superseded v1.66.0.
func TestDifferentVersionsStayActiveTogether(t *testing.T) {
	store := openTestStore(t)
	putAndPromote(t, store, testGeneration("gen_166", "v1.66.0", domain.GenReady), "qdrant")
	putAndPromote(t, store, testGeneration("gen_167", "v1.67.0", domain.GenReady), "qdrant")

	mustState(t, store, "gen_166", domain.GenActive)
	mustState(t, store, "gen_167", domain.GenActive)
	mustActive(t, store, "v1.66.0", "gen_166")
	mustActive(t, store, "v1.67.0", "gen_167")
}

// TestClearActiveGenerationDemotesAndUnpoints is OPS-004's regression
// proof, now per version: clearing one version leaves other versions
// active.
func TestClearActiveGenerationDemotesAndUnpoints(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	putAndPromote(t, store, testGeneration("gen_stale", "v1.67.0", domain.GenReady), "qdrant")
	putAndPromote(t, store, testGeneration("gen_other", "v1.66.0", domain.GenReady), "qdrant")

	if err := store.ClearActiveGeneration(ctx, domain.EcosystemGo, testDep, "v1.67.0", "qdrant"); err != nil {
		t.Fatalf("ClearActiveGeneration: %v", err)
	}

	if _, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, testDep, "v1.67.0", "qdrant"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveGeneration after clear = %v, want ErrNotFound", err)
	}
	mustState(t, store, "gen_stale", domain.GenSuperseded)
	mustActive(t, store, "v1.66.0", "gen_other")
}

func TestClearActiveGenerationOnNothingActiveIsNoop(t *testing.T) {
	store := openTestStore(t)
	if err := store.ClearActiveGeneration(context.Background(), domain.EcosystemGo, testDep, "v1.67.0", "qdrant"); err != nil {
		t.Errorf("ClearActiveGeneration on nothing active = %v, want nil", err)
	}
}

func TestPromoteGenerationPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	putAndPromote(t, s, testGeneration("gen_1", "v1.67.0", domain.GenReady), "qdrant")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	active, err := s2.GetActiveGeneration(ctx, domain.EcosystemGo, testDep, "v1.67.0", "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration after reopen: %v", err)
	}
	if active.ID != "gen_1" || active.State != domain.GenActive {
		t.Errorf("active generation after reopen = %+v", active)
	}
}

func TestActiveGenerationsScopedByBackend(t *testing.T) {
	store := openTestStore(t)
	putAndPromote(t, store, testGeneration("gen_qdrant", "v1.67.0", domain.GenReady), "qdrant")

	if _, err := store.GetActiveGeneration(context.Background(), domain.EcosystemGo, testDep, "v1.67.0", "other-backend"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveGeneration(other-backend) = %v, want ErrNotFound (promotion is backend-scoped)", err)
	}
}

func TestListActivePointersScopesToBackendAndCarriesVersion(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	grpc := testGeneration("gen_grpc", "v1.67.0", domain.GenReady)
	putAndPromote(t, store, grpc, "qdrant")
	if err := store.PromoteGeneration(ctx, grpc, "other"); err != nil {
		t.Fatalf("PromoteGeneration other: %v", err)
	}

	got, err := store.ListActivePointers(ctx, "qdrant")
	if err != nil {
		t.Fatalf("ListActivePointers: %v", err)
	}
	want := ActivePointer{Ecosystem: domain.EcosystemGo, Dependency: testDep, Version: "v1.67.0", GenerationID: "gen_grpc"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("ListActivePointers = %+v, want [%+v]", got, want)
	}
}

func TestListActivePointersEmpty(t *testing.T) {
	got, err := openTestStore(t).ListActivePointers(context.Background(), "qdrant")
	if err != nil {
		t.Fatalf("ListActivePointers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListActivePointers = %+v, want none", got)
	}
}

// seedSchemaV1 writes a database the way a pre-ADR-012 binary left it:
// schema_version 1, one live old-format pointer, and one dangling
// old-format pointer whose generation record is gone.
func seedSchemaV1(t *testing.T, path string) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gen := testGeneration("gen_legacy", "v1.60.0", domain.GenActive)
	if err := s.PutGeneration(context.Background(), gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		active := tx.Bucket([]byte(activeGenerationsBucket))
		if err := active.Put([]byte("go|"+testDep+"|qdrant"), []byte("gen_legacy")); err != nil {
			return err
		}
		if err := active.Put([]byte("go|example.com/gone|qdrant"), []byte("gen_missing")); err != nil {
			return err
		}
		return writeSchemaVersion(tx.Bucket([]byte("meta")), 1)
	})
	if err != nil {
		t.Fatalf("seed v1 state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMigrationRekeysActivePointersByVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	seedSchemaV1(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating): %v", err)
	}
	defer s.Close()

	if v, _ := s.SchemaVersion(ctx); v != 2 {
		t.Errorf("schema version after migration = %d, want 2", v)
	}
	mustActive(t, s, "v1.60.0", "gen_legacy")

	pointers, err := s.ListActivePointers(ctx, "qdrant")
	if err != nil {
		t.Fatalf("ListActivePointers: %v", err)
	}
	var sawDangling bool
	for _, p := range pointers {
		if p.Dependency == "example.com/gone" {
			sawDangling = true
			if p.Version != "" || p.GenerationID != "gen_missing" {
				t.Errorf("dangling pointer = %+v, want empty Version and its original GenerationID", p)
			}
		}
	}
	if !sawDangling {
		t.Error("dangling pre-migration pointer was dropped; it must stay listed so doctor can report it")
	}
}

func TestMigrationIsIdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	seedSchemaV1(t, path)
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		mustActive(t, s, "v1.60.0", "gen_legacy")
		_ = s.Close()
	}
}

func TestNoSourceVersionRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	if has, err := store.HasNoSource(ctx, domain.EcosystemGo, testDep, "v1.67.0"); err != nil || has {
		t.Fatalf("HasNoSource before put = %v, %v; want false, nil", has, err)
	}
	if err := store.PutNoSourceVersion(ctx, NoSourceVersion{Ecosystem: domain.EcosystemGo, Package: testDep, Version: "v1.67.0", Reason: "no manifest"}); err != nil {
		t.Fatalf("PutNoSourceVersion: %v", err)
	}
	if has, _ := store.HasNoSource(ctx, domain.EcosystemGo, testDep, "v1.67.0"); !has {
		t.Error("HasNoSource after put = false, want true")
	}
	if has, _ := store.HasNoSource(ctx, domain.EcosystemGo, testDep, "v1.66.0"); has {
		t.Error("HasNoSource for a different version = true; records must be per version")
	}
	if err := store.DeleteNoSourceVersion(ctx, domain.EcosystemGo, testDep, "v1.67.0"); err != nil {
		t.Fatalf("DeleteNoSourceVersion: %v", err)
	}
	if has, _ := store.HasNoSource(ctx, domain.EcosystemGo, testDep, "v1.67.0"); has {
		t.Error("HasNoSource after delete = true, want false")
	}
}

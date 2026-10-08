package bbolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestGetBackendReplicaNotFound(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.GetBackendReplica(context.Background(), "gen_1", "qdrant"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBackendReplica = %v, want ErrNotFound", err)
	}
}

// TestReplicationUpdatesPointCountIncrementallyToComplete simulates what
// a future replication driver does: PutBackendReplica once per upsert
// batch, incrementing PointCount, then marking Status "complete" once
// every chunk has been sent — the concrete scenario VEC-003 exists to
// make queryable via GetBackendReplica.
func TestReplicationUpdatesPointCountIncrementallyToComplete(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	r := domain.BackendReplica{
		GenerationID:   "gen_1",
		BackendName:    "qdrant",
		EmbeddingModel: "nomic-embed-text",
		Dimensions:     768,
		Status:         "replicating",
	}

	totalChunks := 5
	batches := []int{2, 2, 1} // three upsert batches summing to totalChunks
	for _, batchSize := range batches {
		r.PointCount += batchSize
		r.UpdatedAt = time.Now()
		if err := store.PutBackendReplica(ctx, r); err != nil {
			t.Fatalf("PutBackendReplica: %v", err)
		}

		got, err := store.GetBackendReplica(ctx, r.GenerationID, r.BackendName)
		if err != nil {
			t.Fatalf("GetBackendReplica mid-batch: %v", err)
		}
		if got.PointCount != r.PointCount {
			t.Errorf("PointCount = %d, want %d", got.PointCount, r.PointCount)
		}
		if got.Status != "replicating" {
			t.Errorf("Status = %s, want replicating (not yet done)", got.Status)
		}
	}

	r.Status = "complete"
	if err := store.PutBackendReplica(ctx, r); err != nil {
		t.Fatalf("PutBackendReplica final: %v", err)
	}
	got, err := store.GetBackendReplica(ctx, r.GenerationID, r.BackendName)
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}
	if got.Status != "complete" {
		t.Errorf("Status = %s, want complete", got.Status)
	}
	if got.PointCount != totalChunks {
		t.Errorf("PointCount = %d, want %d", got.PointCount, totalChunks)
	}
}

func TestReplicationFailureSetsStatusFailedWithError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	r := domain.BackendReplica{GenerationID: "gen_1", BackendName: "qdrant", Status: "replicating", PointCount: 2}
	if err := store.PutBackendReplica(ctx, r); err != nil {
		t.Fatalf("PutBackendReplica: %v", err)
	}

	r.Status = "failed"
	r.LastError = "qdrant: backend unavailable: connection refused"
	if err := store.PutBackendReplica(ctx, r); err != nil {
		t.Fatalf("PutBackendReplica failed state: %v", err)
	}

	got, err := store.GetBackendReplica(ctx, r.GenerationID, r.BackendName)
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("Status = %s, want failed", got.Status)
	}
	if got.LastError == "" {
		t.Error("LastError is empty, want a stored failure message")
	}
}

func TestBackendReplicaScopedByGenerationAndBackend(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	a := domain.BackendReplica{GenerationID: "gen_1", BackendName: "qdrant", PointCount: 10}
	b := domain.BackendReplica{GenerationID: "gen_1", BackendName: "other-backend", PointCount: 20}
	c := domain.BackendReplica{GenerationID: "gen_2", BackendName: "qdrant", PointCount: 30}
	for _, r := range []domain.BackendReplica{a, b, c} {
		if err := store.PutBackendReplica(ctx, r); err != nil {
			t.Fatalf("PutBackendReplica: %v", err)
		}
	}

	got, err := store.GetBackendReplica(ctx, "gen_1", "qdrant")
	if err != nil {
		t.Fatalf("GetBackendReplica: %v", err)
	}
	if got.PointCount != 10 {
		t.Errorf("PointCount = %d, want 10 (gen_1/qdrant must not be mixed with gen_1/other-backend or gen_2/qdrant)", got.PointCount)
	}
}

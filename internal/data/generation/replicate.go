package generation

import (
	"context"
	"fmt"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
)

// defaultReplicateBatchSize bounds how many chunks are embedded and
// upserted together per round-trip — also the granularity at which
// BackendReplica.PointCount is persisted, so a crash mid-replication
// loses at most one batch's progress rather than the whole generation.
const defaultReplicateBatchSize = 64

// Replicate embeds every chunk staged for gen (via GEN-002's Build) and
// upserts the resulting vectors into vb under ns, tracking progress in a
// domain.BackendReplica (VEC-003) as it goes. This is the driver GEN-002
// and VEC-003 each assumed would exist elsewhere — neither ticket
// specified it, so it lives here, alongside Build, as generation's other
// lifecycle-advancing entry point.
//
// Any embedding or upsert failure marks the replica FAILED with the
// error recorded and returns immediately — a partially replicated
// generation is never reported as complete. Re-running Replicate after a
// failure starts over (no partial-batch resume in v0.1); embedding
// itself is cheap to repeat if embedder is wrapped in
// internal/embedding/cache.CachingEmbedder, since unchanged chunk
// content will hit its cache.
func Replicate(ctx context.Context, gen domain.Generation, embedder embedding.Embedder, vb backend.VectorBackend, ns backend.Namespace, store *bbolt.Store, badgerStore *badger.Store) error {
	replica := domain.BackendReplica{
		GenerationID:   gen.ID,
		BackendName:    vb.Name(),
		EmbeddingModel: embedder.ModelID(),
		Dimensions:     ns.Dimensions,
		Status:         "replicating",
		UpdatedAt:      time.Now(),
	}
	if err := store.PutBackendReplica(ctx, replica); err != nil {
		return fmt.Errorf("%w: persist initial replica state: %v", ErrReplication, err)
	}

	if err := vb.EnsureNamespace(ctx, ns); err != nil {
		return failReplica(ctx, store, &replica, fmt.Errorf("%w: ensure namespace %s: %v", ErrReplication, ns.Name, err))
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		return failReplica(ctx, store, &replica, fmt.Errorf("%w: list chunks for %s: %v", ErrReplication, gen.ID, err))
	}

	objectCache := map[string]domain.KnowledgeObject{}
	for start := 0; start < len(chunks); start += defaultReplicateBatchSize {
		end := min(start+defaultReplicateBatchSize, len(chunks))
		batch := chunks[start:end]

		points, err := embedBatch(ctx, embedder, badgerStore, objectCache, gen, batch)
		if err != nil {
			return failReplica(ctx, store, &replica, err)
		}

		if err := vb.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: points}); err != nil {
			return failReplica(ctx, store, &replica, fmt.Errorf("%w: upsert batch: %v", ErrReplication, err))
		}

		replica.PointCount += len(points)
		replica.UpdatedAt = time.Now()
		if err := store.PutBackendReplica(ctx, replica); err != nil {
			return failReplica(ctx, store, &replica, fmt.Errorf("%w: persist progress: %v", ErrReplication, err))
		}
	}

	replica.Status = "complete"
	replica.UpdatedAt = time.Now()
	if err := store.PutBackendReplica(ctx, replica); err != nil {
		return fmt.Errorf("%w: persist final replica state: %v", ErrReplication, err)
	}
	return nil
}

// embedBatch resolves each chunk's parent KnowledgeObject (cached across
// calls, since many chunks share one object) for its source_type/
// authority, embeds the chunks' text, and returns one backend.Point per
// chunk.
//
// Ecosystem/Dependency/Version on the resulting point come from gen, not
// from the object's own obj.Dependency field: GEN-003's content reuse
// means an object can be shared by multiple generations at different
// versions, so obj.Dependency only reflects whichever generation first
// created it — using it here would silently mislabel a reused chunk's
// vector with a stale version, defeating the whole point of the
// "generation"/"version" metadata filters VAL-003 relies on.
func embedBatch(ctx context.Context, embedder embedding.Embedder, badgerStore *badger.Store, objectCache map[string]domain.KnowledgeObject, gen domain.Generation, chunks []domain.Chunk) ([]backend.Point, error) {
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = string(c.Content)
	}

	vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("%w: embed batch: %v", ErrReplication, err)
	}
	if len(vectors) != len(chunks) {
		return nil, fmt.Errorf("%w: embedder returned %d vectors for %d chunks", ErrReplication, len(vectors), len(chunks))
	}

	points := make([]backend.Point, len(chunks))
	for i, c := range chunks {
		obj, ok := objectCache[c.ObjectID]
		if !ok {
			obj, err = badgerStore.GetKnowledgeObject(ctx, c.ObjectID)
			if err != nil {
				return nil, fmt.Errorf("%w: load object %s for chunk %s: %v", ErrReplication, c.ObjectID, c.ID, err)
			}
			objectCache[c.ObjectID] = obj
		}

		points[i] = backend.Point{
			ID:     c.ID,
			Vector: vectors[i],
			Metadata: backend.PointMetadata{
				Ecosystem:  string(gen.Dependency.Dependency.Ecosystem),
				Dependency: gen.Dependency.Dependency.Name,
				Version:    gen.Dependency.Version,
				Generation: gen.ID,
				SourceType: obj.SourceType,
				Authority:  obj.Authority,
			},
		}
	}
	return points, nil
}

// failReplica persists replica as FAILED with err's message and returns
// err unchanged, so callers can both classify (errors.Is) and propagate
// in one line.
func failReplica(ctx context.Context, store *bbolt.Store, replica *domain.BackendReplica, err error) error {
	replica.Status = "failed"
	replica.LastError = err.Error()
	replica.UpdatedAt = time.Now()
	if putErr := store.PutBackendReplica(ctx, *replica); putErr != nil {
		// Metadata is advisory bookkeeping; the backend write (or lack
		// thereof) is the source of truth, so a failure to record the
		// failure itself doesn't change what we return.
		fmt.Printf("generation: failed to persist FAILED replica state for %s/%s: %v\n", replica.GenerationID, replica.BackendName, putErr)
	}
	return err
}

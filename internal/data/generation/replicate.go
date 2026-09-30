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
	"aleutian-ai/ragctl/internal/observability"
	"aleutian-ai/ragctl/internal/observability/metrics"
	"aleutian-ai/ragctl/internal/observability/trace"
	"aleutian-ai/ragctl/internal/registry"
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
//
// sources is the dependency's *current* registry match (the same list
// Build was called with, or a freshly re-resolved one) — used to stamp
// each point's SourceType/Authority from the current source config
// rather than from whichever object happens to be stored, for the same
// reason gen.Dependency (not obj.Dependency) is used for version: GEN-003
// content reuse means a chunk's parent KnowledgeObject may have been
// created by an earlier generation under whatever source
// config was current then, and authority is user/registry-configurable
// (see internal/registry's Loader — a user override can change it
// between syncs). A source present in the object but absent from
// sources (e.g. removed from config since acquisition) falls back to
// the object's own stored fields rather than losing that metadata.
func Replicate(ctx context.Context, gen domain.Generation, sources []registry.Source, embedder embedding.Embedder, vb backend.VectorBackend, ns backend.Namespace, store *bbolt.Store, badgerStore *badger.Store) error {
	ctx, replicateSpanEnd := trace.StartSpan(ctx, "replicate")
	defer replicateSpanEnd()
	replicateStart := time.Now()
	logger := observability.FromContext(ctx).With(
		observability.KeyDependency, gen.Dependency.Dependency.Name,
		observability.KeyVersion, gen.Dependency.Version,
		observability.KeyGeneration, gen.ID,
		observability.KeyBackend, vb.Name(),
	)
	var embedDuration time.Duration
	var embeddedChunks int

	sourcesByID := make(map[string]registry.Source, len(sources))
	for _, s := range sources {
		sourcesByID[s.ID] = s
	}
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
		return failReplica(ctx, store, &replica, &gen, fmt.Errorf("%w: ensure namespace %s: %v", ErrReplication, ns.Name, err))
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		return failReplica(ctx, store, &replica, &gen, fmt.Errorf("%w: list chunks for %s: %v", ErrReplication, gen.ID, err))
	}

	reportProgress(ctx, 0, len(chunks))
	objectCache := map[string]domain.KnowledgeObject{}
	for start := 0; start < len(chunks); start += defaultReplicateBatchSize {
		end := min(start+defaultReplicateBatchSize, len(chunks))
		batch := chunks[start:end]

		embedCtx, embedEnd := trace.StartSpan(ctx, "embed")
		embedStart := time.Now()
		points, err := embedBatch(embedCtx, embedder, badgerStore, objectCache, gen, sourcesByID, batch)
		metrics.EmbedSeconds.Observe(time.Since(embedStart).Seconds())
		embedDuration += time.Since(embedStart)
		embeddedChunks += len(batch)
		if err != nil {
			trace.RecordError(embedCtx, err)
			embedEnd()
			logger.Error("embed failed", observability.KeyStage, "embed", observability.KeyGenAIOperationName, "embeddings", observability.KeyEmbeddingModelName, embedder.ModelID(), "error", err)
			return failReplica(ctx, store, &replica, &gen, err)
		}
		embedEnd()

		upsertStart := time.Now()
		err = vb.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: points})
		metrics.BackendUpsertSeconds.Observe(time.Since(upsertStart).Seconds())
		if err != nil {
			return failReplica(ctx, store, &replica, &gen, fmt.Errorf("%w: upsert batch: %v", ErrReplication, err))
		}

		reportProgress(ctx, end, len(chunks))
		replica.PointCount += len(points)
		replica.UpdatedAt = time.Now()
		if err := store.PutBackendReplica(ctx, replica); err != nil {
			return failReplica(ctx, store, &replica, &gen, fmt.Errorf("%w: persist progress: %v", ErrReplication, err))
		}
	}

	replica.Status = "complete"
	replica.UpdatedAt = time.Now()
	if err := store.PutBackendReplica(ctx, replica); err != nil {
		return fmt.Errorf("%w: persist final replica state: %v", ErrReplication, err)
	}

	logger.Info("embed completed",
		observability.KeyStage, "embed",
		observability.KeyGenAIOperationName, "embeddings",
		observability.KeyEmbeddingModelName, embedder.ModelID(),
		observability.KeyDurationMS, embedDuration.Milliseconds(),
		"chunks", embeddedChunks,
	)
	logger.Info("replicate completed",
		observability.KeyStage, "replicate",
		observability.KeyDurationMS, time.Since(replicateStart).Milliseconds(),
		"points", replica.PointCount,
	)
	return nil
}

// embedBatch resolves each chunk's parent KnowledgeObject (cached across
// calls, since many chunks share one object), embeds the chunks' text,
// and returns one backend.Point per chunk.
//
// Every metadata field on the resulting point — ecosystem, dependency,
// version, generation, source_type, authority — comes from gen and
// sourcesByID (the current generation's own state), never read directly
// off the object: GEN-003 content reuse means an object's stored fields
// only reflect whichever generation first created it, so trusting them
// here would silently mislabel a reused chunk's vector with stale
// metadata — the same failure mode already found and fixed for Version;
// SourceType/Authority had the identical bug and are fixed the same way.
func embedBatch(ctx context.Context, embedder embedding.Embedder, badgerStore *badger.Store, objectCache map[string]domain.KnowledgeObject, gen domain.Generation, sourcesByID map[string]registry.Source, chunks []domain.Chunk) ([]backend.Point, error) {
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

		sourceType, authority := obj.SourceType, obj.Authority
		if s, ok := sourcesByID[obj.SourceID]; ok {
			sourceType, authority = s.Type, s.Authority
		}

		points[i] = backend.Point{
			ID:     c.ID,
			Vector: vectors[i],
			Metadata: backend.PointMetadata{
				Ecosystem:  string(gen.Dependency.Dependency.Ecosystem),
				Dependency: gen.Dependency.Dependency.Name,
				Version:    gen.Dependency.Version,
				Generation: gen.ID,
				SourceType: sourceType,
				Authority:  authority,
			},
		}
	}
	return points, nil
}

// failReplica persists replica as FAILED with err's message, and also
// transitions gen itself to FAILED (fail, generation.go) — before this
// fix, only the BackendReplica record was marked FAILED on a Replicate-
// stage error, leaving the Generation record stuck at whatever state
// Build last set it to (INDEXING). Functionally harmless for query
// correctness (an INDEXING-state generation was never promoted or
// served either way — confirmed by VALID-001's own test before this
// fix), but a real observability gap: anything inspecting
// Generation.State directly (status/doctor, orphan GC's own candidate
// discovery) saw a generation stuck mid-build, not visibly failed.
// Returns err unchanged so callers can both classify (errors.Is) and
// propagate in one line.
func failReplica(ctx context.Context, store *bbolt.Store, replica *domain.BackendReplica, gen *domain.Generation, err error) error {
	replica.Status = "failed"
	replica.LastError = err.Error()
	replica.UpdatedAt = time.Now()
	if putErr := store.PutBackendReplica(ctx, *replica); putErr != nil {
		// Metadata is advisory bookkeeping; the backend write (or lack
		// thereof) is the source of truth, so a failure to record the
		// failure itself doesn't change what we return.
		fmt.Printf("generation: failed to persist FAILED replica state for %s/%s: %v\n", replica.GenerationID, replica.BackendName, putErr)
	}
	fail(ctx, store, gen, err.Error())
	return err
}

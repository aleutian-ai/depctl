// Package generation drives a resolved dependency version through
// acquisition, normalization, fingerprinting, and chunking into a staged
// knowledge Generation (Build), then writes its chunks into the search
// indexes (Replicate, AddToIndex), ready for validation and promotion.
package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/oklog/ulid/v2"

	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
)

// Manifest is a generation's Badger-resident summary: what it was built
// from and how much content it holds. Kept separate from domain.Generation
// (the bbolt lifecycle record) because the manifest grows with build
// progress (object/chunk counts) while the bbolt record only tracks state.
type Manifest struct {
	ID             string                   `json:"id"`
	Dependency     domain.DependencyVersion `json:"dependency"`
	Sources        []string                 `json:"sources"`
	ObjectCount    int                      `json:"object_count"`
	ChunkCount     int                      `json:"chunk_count"`
	ObjectsReused  int                      `json:"objects_reused"`
	ObjectsCreated int                      `json:"objects_created"`
	EmbeddingModel string                   `json:"embedding_model"`
	CreatedAt      time.Time                `json:"created_at"`
}

// Create writes a new PLANNED generation record to bbolt and an empty
// manifest skeleton to Badger, so later pipeline stages (Build) have a
// target to write into.
//
// The Badger manifest is written before the bbolt record: if the bbolt
// write then fails, the orphaned manifest is harmless (unreferenced,
// eventually GC-able) — but a bbolt record with no manifest would leave
// later stages unable to find anything to build into.
func Create(ctx context.Context, store *bbolt.Store, badgerStore *badger.Store, dep domain.DependencyVersion) (domain.Generation, error) {
	now := time.Now()
	id := "gen_" + ulid.Make().String()

	manifest := Manifest{ID: id, Dependency: dep, CreatedAt: now}
	data, err := json.Marshal(manifest)
	if err != nil {
		return domain.Generation{}, fmt.Errorf("generation: marshal manifest for %s: %w", id, err)
	}
	if err := badgerStore.PutManifest(ctx, id, data); err != nil {
		return domain.Generation{}, fmt.Errorf("generation: write manifest for %s: %w", id, err)
	}

	gen := domain.Generation{
		ID:         id,
		Dependency: dep,
		State:      domain.GenPlanned,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.PutGeneration(ctx, gen); err != nil {
		log.Printf("generation: orphaned manifest %s after bbolt write failure: %v", id, err)
		return domain.Generation{}, fmt.Errorf("generation: write record %s: %w", id, err)
	}

	return gen, nil
}

// getManifest reads and decodes generationID's manifest from Badger.
func getManifest(ctx context.Context, badgerStore *badger.Store, generationID string) (Manifest, error) {
	data, err := badgerStore.GetManifest(ctx, generationID)
	if err != nil {
		return Manifest{}, fmt.Errorf("generation: read manifest %s: %w", generationID, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("generation: decode manifest %s: %w", generationID, err)
	}
	return m, nil
}

// putManifest encodes and writes m back to Badger.
func putManifest(ctx context.Context, badgerStore *badger.Store, m Manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("generation: marshal manifest %s: %w", m.ID, err)
	}
	if err := badgerStore.PutManifest(ctx, m.ID, data); err != nil {
		return fmt.Errorf("generation: write manifest %s: %w", m.ID, err)
	}
	return nil
}

// setState transitions gen to state in bbolt, updating UpdatedAt.
func setState(ctx context.Context, store *bbolt.Store, gen *domain.Generation, state domain.GenerationState) error {
	gen.State = state
	gen.UpdatedAt = time.Now()
	if err := store.PutGeneration(ctx, *gen); err != nil {
		return fmt.Errorf("generation: persist state %s for %s: %w", state, gen.ID, err)
	}
	return nil
}

// fail transitions gen to FAILED with msg recorded, best-effort (a
// failure persisting the FAILED state itself is logged, not returned —
// the original error is what callers need to see).
func fail(ctx context.Context, store *bbolt.Store, gen *domain.Generation, msg string) {
	gen.State = domain.GenFailed
	gen.Error = msg
	gen.UpdatedAt = time.Now()
	if err := store.PutGeneration(ctx, *gen); err != nil {
		log.Printf("generation: failed to persist FAILED state for %s: %v", gen.ID, err)
	}
}

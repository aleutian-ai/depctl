package bbolt

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/domain"
)

const generationsBucket = "generations"

// PutGeneration upserts a generation record, keyed by its ID.
func (s *Store) PutGeneration(ctx context.Context, g domain.Generation) error {
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("marshal generation %s: %w", g.ID, err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(generationsBucket)).Put([]byte(g.ID), data)
	})
}

// GetGeneration returns the generation with the given ID, or ErrNotFound.
func (s *Store) GetGeneration(ctx context.Context, id string) (domain.Generation, error) {
	var g domain.Generation
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(generationsBucket)).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &g)
	})
	return g, err
}

// DeleteGenerationRecord removes the generation record with the given
// ID. Deleting an already-absent record is not an error — GC (RET-004)
// steps are idempotent by design, so a retried delete after a partial
// prior run must succeed silently.
func (s *Store) DeleteGenerationRecord(ctx context.Context, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(generationsBucket)).Delete([]byte(id))
	})
}

// ListGenerationsByDependencyVersion returns every generation recorded
// for (ecosystem, pkg, version) — GC (RET-004) needs this to find which
// generation IDs' Badger data to delete for a GC-eligible version, since
// no index from (ecosystem, package, version) to generation ID exists
// (the `generations` bucket is keyed by generation ID only); a full
// bucket scan is the substitute, same "small keyspace" tradeoff already
// made for references (RET-001's ListAllReferences) and GC candidate
// discovery (RET-003's PlanGC).
// ListAllGenerations returns every generation record in the bucket —
// GC-001's orphan planner needs a fleet-wide scan since an orphaned
// generation, by definition, was never promoted and so has no
// dependency+version index pointing at it the way
// ListGenerationsByDependencyVersion requires already knowing which
// (ecosystem, package, version) to ask for.
func (s *Store) ListAllGenerations(ctx context.Context) ([]domain.Generation, error) {
	var gens []domain.Generation
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(generationsBucket)).ForEach(func(k, v []byte) error {
			var g domain.Generation
			if err := json.Unmarshal(v, &g); err != nil {
				return fmt.Errorf("unmarshal generation %s: %w", k, err)
			}
			gens = append(gens, g)
			return nil
		})
	})
	return gens, err
}

func (s *Store) ListGenerationsByDependencyVersion(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.Generation, error) {
	var gens []domain.Generation
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(generationsBucket)).ForEach(func(k, v []byte) error {
			var g domain.Generation
			if err := json.Unmarshal(v, &g); err != nil {
				return fmt.Errorf("unmarshal generation %s: %w", k, err)
			}
			if g.Dependency.Dependency.Ecosystem == ecosystem && g.Dependency.Dependency.Name == pkg && g.Dependency.Version == version {
				gens = append(gens, g)
			}
			return nil
		})
	})
	return gens, err
}

package bbolt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/domain"
)

const activeGenerationsBucket = "active_generations"

// ActivePointer is one active_generations entry: the dependency it is
// scoped to and the generation ID it points at. The pointed-to record is
// deliberately not resolved here, so callers like `ragctl doctor` can see
// a pointer whose generation record has gone missing.
type ActivePointer struct {
	Ecosystem    domain.Ecosystem
	Dependency   string
	GenerationID string
}

// activeGenerationKey identifies a dependency+backend's active-generation
// pointer. Pipe-separated (matching internal/registry's own
// ecosystem+package index key convention) rather than slash-separated,
// since a dependency name itself commonly contains slashes (e.g.
// "google.golang.org/grpc") and would otherwise be ambiguous with the
// backend-name field boundary.
func activeGenerationKey(ecosystem domain.Ecosystem, dependencyName, backendName string) []byte {
	return []byte(string(ecosystem) + "|" + dependencyName + "|" + backendName)
}

// PromoteGeneration atomically promotes candidate to ACTIVE for its
// dependency+backendName, superseding whichever generation was
// previously active for that same (dependency, backend) pair — a single
// bbolt Update touching exactly: the prior active record (if any, state
// -> SUPERSEDED), the candidate record (state -> ACTIVE), and the
// active_generations pointer. No network/remote calls happen inside this
// transaction; candidate must already reflect everything upstream
// acquisition/embedding/replication work.
func (s *Store) PromoteGeneration(ctx context.Context, candidate domain.Generation, backendName string) error {
	now := time.Now()
	key := activeGenerationKey(candidate.Dependency.Dependency.Ecosystem, candidate.Dependency.Dependency.Name, backendName)

	return s.db.Update(func(tx *bolt.Tx) error {
		genBucket := tx.Bucket([]byte(generationsBucket))
		activeBucket := tx.Bucket([]byte(activeGenerationsBucket))

		if priorID := activeBucket.Get(key); priorID != nil {
			priorData := genBucket.Get(priorID)
			if priorData != nil {
				var prior domain.Generation
				if err := json.Unmarshal(priorData, &prior); err != nil {
					return fmt.Errorf("unmarshal prior active generation %s: %w", priorID, err)
				}
				prior.State = domain.GenSuperseded
				prior.UpdatedAt = now
				data, err := json.Marshal(prior)
				if err != nil {
					return fmt.Errorf("marshal superseded generation %s: %w", prior.ID, err)
				}
				if err := genBucket.Put(priorID, data); err != nil {
					return err
				}
			}
		}

		candidate.State = domain.GenActive
		candidate.UpdatedAt = now
		data, err := json.Marshal(candidate)
		if err != nil {
			return fmt.Errorf("marshal promoted generation %s: %w", candidate.ID, err)
		}
		if err := genBucket.Put([]byte(candidate.ID), data); err != nil {
			return err
		}

		return activeBucket.Put(key, []byte(candidate.ID))
	})
}

// GetActiveGeneration returns the currently active generation for
// (ecosystem, dependencyName, backendName), or ErrNotFound if none has
// been promoted yet.
func (s *Store) GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, dependencyName, backendName string) (domain.Generation, error) {
	var gen domain.Generation
	err := s.db.View(func(tx *bolt.Tx) error {
		key := activeGenerationKey(ecosystem, dependencyName, backendName)
		id := tx.Bucket([]byte(activeGenerationsBucket)).Get(key)
		if id == nil {
			return ErrNotFound
		}
		data := tx.Bucket([]byte(generationsBucket)).Get(id)
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &gen)
	})
	return gen, err
}

// ListActivePointers returns every active-generation pointer scoped to
// backendName, across all dependencies.
func (s *Store) ListActivePointers(ctx context.Context, backendName string) ([]ActivePointer, error) {
	suffix := "|" + backendName
	var pointers []ActivePointer
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(activeGenerationsBucket)).ForEach(func(k, v []byte) error {
			key, ok := strings.CutSuffix(string(k), suffix)
			if !ok {
				return nil
			}
			ecosystem, dependency, ok := strings.Cut(key, "|")
			if !ok {
				return fmt.Errorf("malformed active generation key %q", k)
			}
			pointers = append(pointers, ActivePointer{
				Ecosystem:    domain.Ecosystem(ecosystem),
				Dependency:   dependency,
				GenerationID: string(v),
			})
			return nil
		})
	})
	return pointers, err
}

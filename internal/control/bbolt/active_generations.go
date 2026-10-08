package bbolt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const activeGenerationsBucket = "active_generations"

// ActivePointer is one active_generations entry: the dependency version
// it is scoped to and the generation ID it points at. The pointed-to
// record is deliberately not resolved here, so callers like `depctl
// doctor` can see a pointer whose generation record has gone missing.
type ActivePointer struct {
	Ecosystem    domain.Ecosystem
	Dependency   string
	Version      string
	GenerationID string
}

// activeGenerationKey identifies one dependency version's active pointer
// for a backend. Keyed by version (ADR-012): several versions of one
// dependency can be active at once when different projects reference
// them. Pipe-separated rather than slash-separated, since dependency names
// commonly contain slashes ("google.golang.org/grpc"); none contains a
// pipe, so the four fields split unambiguously.
func activeGenerationKey(ecosystem domain.Ecosystem, dependencyName, version, backendName string) []byte {
	return []byte(string(ecosystem) + "|" + dependencyName + "|" + version + "|" + backendName)
}

// PromoteGeneration atomically promotes candidate to ACTIVE for its exact
// dependency version and backendName, superseding only the generation
// previously active for that same version (a rebuild). Other versions of
// the dependency stay active: their lifetime belongs to references and
// retention, not to promotion (ADR-012). One bbolt Update touching
// exactly the prior same-version record (if any, -> SUPERSEDED), the
// candidate record (-> ACTIVE), and the pointer. No network calls happen
// inside this transaction.
func (s *Store) PromoteGeneration(ctx context.Context, candidate domain.Generation, backendName string) error {
	now := time.Now()
	key := activeGenerationKey(candidate.Dependency.Dependency.Ecosystem, candidate.Dependency.Dependency.Name, candidate.Dependency.Version, backendName)

	return s.db.Update(func(tx *bolt.Tx) error {
		genBucket := tx.Bucket([]byte(generationsBucket))
		activeBucket := tx.Bucket([]byte(activeGenerationsBucket))

		if priorID := activeBucket.Get(key); priorID != nil {
			if err := markSuperseded(genBucket, priorID, now); err != nil {
				return err
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

// ClearActiveGeneration removes the active pointer for one exact
// dependency version, demoting whatever generation it pointed at to
// SUPERSEDED. OPS-004: the only way to make the planner treat that
// version as unbuilt, forcing a genuine rebuild of a generation whose
// backend content is gone despite healthy-looking bookkeeping. Other
// versions of the dependency are untouched. A no-op, not an error, if
// nothing is active for that version.
func (s *Store) ClearActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, dependencyName, version, backendName string) error {
	key := activeGenerationKey(ecosystem, dependencyName, version, backendName)
	return s.db.Update(func(tx *bolt.Tx) error {
		activeBucket := tx.Bucket([]byte(activeGenerationsBucket))
		id := activeBucket.Get(key)
		if id == nil {
			return nil
		}
		if err := markSuperseded(tx.Bucket([]byte(generationsBucket)), id, time.Now()); err != nil {
			return err
		}
		return activeBucket.Delete(key)
	})
}

// GetActiveGeneration returns the active generation for one exact
// dependency version and backend, or ErrNotFound if that version has
// none. There is deliberately no version-less variant (ADR-012): "is
// anything active for this dependency?" is the question that let a
// version change go unnoticed.
func (s *Store) GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, dependencyName, version, backendName string) (domain.Generation, error) {
	var gen domain.Generation
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket([]byte(activeGenerationsBucket)).Get(activeGenerationKey(ecosystem, dependencyName, version, backendName))
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

// ListActivePointers returns every active pointer scoped to backendName,
// across all dependencies and versions.
func (s *Store) ListActivePointers(ctx context.Context, backendName string) ([]ActivePointer, error) {
	var pointers []ActivePointer
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(activeGenerationsBucket)).ForEach(func(k, v []byte) error {
			parts := strings.Split(string(k), "|")
			var p ActivePointer
			var backend string
			switch len(parts) {
			case 4:
				p = ActivePointer{Ecosystem: domain.Ecosystem(parts[0]), Dependency: parts[1], Version: parts[2]}
				backend = parts[3]
			case 3:
				// A pre-ADR-012 pointer migration 2 couldn't rekey because
				// its generation record is missing. Listed with an empty
				// Version so `depctl doctor` can still report it.
				p = ActivePointer{Ecosystem: domain.Ecosystem(parts[0]), Dependency: parts[1]}
				backend = parts[2]
			default:
				return fmt.Errorf("malformed active generation key %q", k)
			}
			if backend != backendName {
				return nil
			}
			p.GenerationID = string(v)
			pointers = append(pointers, p)
			return nil
		})
	})
	return pointers, err
}

// markSuperseded sets the generation record id (if present) to
// SUPERSEDED. A missing record is not an error: the pointer is being
// replaced or removed either way.
func markSuperseded(genBucket *bolt.Bucket, id []byte, now time.Time) error {
	raw := genBucket.Get(id)
	if raw == nil {
		return nil
	}
	var g domain.Generation
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("unmarshal generation %s: %w", id, err)
	}
	g.State = domain.GenSuperseded
	g.UpdatedAt = now
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("marshal superseded generation %s: %w", g.ID, err)
	}
	return genBucket.Put(id, data)
}

// migrateActivePointersPerVersion is schema migration 2 (ADR-012,
// STORE-005): rewrites old three-part pointer keys
// (ecosystem|dependency|backend) under the per-version four-part key,
// taking the version from the pointed-to generation record. A pointer
// whose generation record is missing can't be rekeyed and is left as-is
// for `depctl doctor` to report. Runs inside ensureSchema's transaction,
// so it's all-or-nothing.
func migrateActivePointersPerVersion(tx *bolt.Tx) error {
	activeBucket := tx.Bucket([]byte(activeGenerationsBucket))
	genBucket := tx.Bucket([]byte(generationsBucket))

	type rekey struct{ oldKey, newKey, id []byte }
	var pending []rekey
	err := activeBucket.ForEach(func(k, v []byte) error {
		parts := strings.Split(string(k), "|")
		if len(parts) != 3 {
			return nil
		}
		raw := genBucket.Get(v)
		if raw == nil {
			return nil
		}
		var g domain.Generation
		if err := json.Unmarshal(raw, &g); err != nil {
			return fmt.Errorf("unmarshal generation %s: %w", v, err)
		}
		newKey := activeGenerationKey(domain.Ecosystem(parts[0]), parts[1], g.Dependency.Version, parts[2])
		pending = append(pending, rekey{oldKey: append([]byte(nil), k...), newKey: newKey, id: append([]byte(nil), v...)})
		return nil
	})
	if err != nil {
		return err
	}
	// Mutating a bucket while iterating it with ForEach is undefined, so
	// collect first, then apply.
	for _, r := range pending {
		if err := activeBucket.Put(r.newKey, r.id); err != nil {
			return err
		}
		if err := activeBucket.Delete(r.oldKey); err != nil {
			return err
		}
	}
	return nil
}

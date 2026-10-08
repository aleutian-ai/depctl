package bbolt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const referencesBucket = "references"

// referenceKey identifies one (ecosystem, package, version) tuple's
// reference from one (projectID, reason) pair — multiple reasons can
// reference the same version simultaneously (e.g. a project reference
// and, once dropped, a grace_period reference can briefly coexist), and
// the same version can be referenced by many projects at once. Version
// sits before project/reason so every reference for one version is a
// contiguous prefix scan (what CountReferences/ListReferences need);
// pipe-separated, matching internal/registry's own index-key convention,
// since package names commonly contain slashes.
func referenceKey(ecosystem domain.Ecosystem, pkg, version, projectID string, reason domain.ReferenceReason) []byte {
	return []byte(string(ecosystem) + "|" + pkg + "|" + version + "|" + projectID + "|" + string(reason))
}

func referenceVersionPrefix(ecosystem domain.Ecosystem, pkg, version string) []byte {
	return []byte(string(ecosystem) + "|" + pkg + "|" + version + "|")
}

func referenceProjectAndVersionPrefix(ecosystem domain.Ecosystem, pkg, version, projectID string) []byte {
	return []byte(string(ecosystem) + "|" + pkg + "|" + version + "|" + projectID + "|")
}

// AddReference upserts r, keyed by (ecosystem, package, version,
// projectID, reason) — adding the same reference twice is idempotent
// (FirstSeenAt is preserved from the existing record, LastSeenAt
// refreshed), never an error.
func (s *Store) AddReference(ctx context.Context, r domain.VersionReference) error {
	key := referenceKey(r.Ecosystem, r.Package, r.Version, r.ProjectID, r.Reason)
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(referencesBucket))
		if existing := b.Get(key); existing != nil {
			var prior domain.VersionReference
			if err := json.Unmarshal(existing, &prior); err == nil {
				r.FirstSeenAt = prior.FirstSeenAt
			}
		}
		data, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshal reference %s/%s/%s: %w", r.Ecosystem, r.Package, r.Version, err)
		}
		return b.Put(key, data)
	})
}

// RemoveReference deletes every reference (any Reason) that
// (ecosystem, pkg, version, projectID) recorded. Removing a
// non-existent reference is a no-op, not an error.
func (s *Store) RemoveReference(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, projectID string) error {
	prefix := referenceProjectAndVersionPrefix(ecosystem, pkg, version, projectID)
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(referencesBucket))
		c := b.Cursor()
		var toDelete [][]byte
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			toDelete = append(toDelete, append([]byte(nil), k...))
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// CountReferences returns how many reference records (any reason, any
// project) exist for (ecosystem, pkg, version).
func (s *Store) CountReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) (int, error) {
	refs, err := s.ListReferences(ctx, ecosystem, pkg, version)
	return len(refs), err
}

// ListReferences returns every reference record for (ecosystem, pkg,
// version), across every project and reason.
func (s *Store) ListReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.VersionReference, error) {
	var refs []domain.VersionReference
	prefix := referenceVersionPrefix(ecosystem, pkg, version)
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(referencesBucket)).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var r domain.VersionReference
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("unmarshal reference %s: %w", k, err)
			}
			refs = append(refs, r)
		}
		return nil
	})
	return refs, err
}

// ListProjectReferences returns projectID's own "project"-reason
// references (its current per-dependency version pins) — what the
// planner (PLAN-001) diffs a fresh Resolution against. Not part of
// RET-001's own method list (which is all version-keyed, not
// project-keyed); added because something has to answer "what does this
// project currently reference" and the reference bucket is project ID's
// *last* key segment, not a prefix, so this is a full bucket scan rather
// than a prefix seek — acceptable per RET-001's own "small keyspace"
// simplicity note.
func (s *Store) ListProjectReferences(ctx context.Context, projectID string) ([]domain.VersionReference, error) {
	var refs []domain.VersionReference
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(referencesBucket)).ForEach(func(k, v []byte) error {
			var r domain.VersionReference
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("unmarshal reference %s: %w", k, err)
			}
			if r.ProjectID == projectID && r.Reason == domain.ReferenceReasonProject {
				refs = append(refs, r)
			}
			return nil
		})
	})
	return refs, err
}

// DeleteAllReferences removes every reference record (any project, any
// reason) for (ecosystem, pkg, version) — the reference-metadata cleanup
// step of GC (RET-004), run only after the version's vector/Badger data
// is already gone.
func (s *Store) DeleteAllReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) error {
	prefix := referenceVersionPrefix(ecosystem, pkg, version)
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(referencesBucket))
		c := b.Cursor()
		var toDelete [][]byte
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			toDelete = append(toDelete, append([]byte(nil), k...))
		}
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListAllReferences returns every reference record in the bucket,
// across every ecosystem/package/version/project/reason — the
// substitute RET-003's GC planner uses for "iterate the
// dependency_versions bucket": that bucket (STORE-001's fuller
// relational split) was never built (see STORE-001's own gap note), and
// the references bucket is the only place a (ecosystem, package,
// version) tuple is actually recorded once a project starts depending
// on it, so enumerating distinct tuples from here is the real substitute
// for iterating a versions bucket that doesn't exist.
func (s *Store) ListAllReferences(ctx context.Context) ([]domain.VersionReference, error) {
	var refs []domain.VersionReference
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(referencesBucket)).ForEach(func(k, v []byte) error {
			var r domain.VersionReference
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("unmarshal reference %s: %w", k, err)
			}
			refs = append(refs, r)
			return nil
		})
	})
	return refs, err
}

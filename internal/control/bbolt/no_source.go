package bbolt

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const noSourceVersionsBucket = "no_source_versions"

// NoSourceVersion records that sync determined a dependency version has
// no usable docs source: no registry manifest and no fallback source
// (PLAN-005). Deliberately its own record, not a FAILED generation: a
// failed build is retried on the next sync, a no-source version is not,
// and the two must never be confused by anything that retries failures.
type NoSourceVersion struct {
	Ecosystem  domain.Ecosystem
	Package    string
	Version    string
	Reason     string
	RecordedAt time.Time
}

func noSourceKey(ecosystem domain.Ecosystem, pkg, version string) []byte {
	return []byte(string(ecosystem) + "|" + pkg + "|" + version)
}

// PutNoSourceVersion records (or refreshes) a no-source determination.
func (s *Store) PutNoSourceVersion(ctx context.Context, rec NoSourceVersion) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal no-source record for %s@%s: %w", rec.Package, rec.Version, err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(noSourceVersionsBucket)).Put(noSourceKey(rec.Ecosystem, rec.Package, rec.Version), data)
	})
}

// HasNoSource reports whether this exact dependency version is recorded
// as having no docs source.
func (s *Store) HasNoSource(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) (bool, error) {
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket([]byte(noSourceVersionsBucket)).Get(noSourceKey(ecosystem, pkg, version)) != nil
		return nil
	})
	return found, err
}

// DeleteNoSourceVersion removes a no-source record, e.g. once that
// version builds successfully. A no-op if none exists.
func (s *Store) DeleteNoSourceVersion(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(noSourceVersionsBucket)).Delete(noSourceKey(ecosystem, pkg, version))
	})
}

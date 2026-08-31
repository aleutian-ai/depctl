package bbolt

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/domain"
)

// resolutionsBucket stores one domain.Resolution per project, keyed by
// project ID. This is an interim simplification of STORE-001's fuller
// project_dependencies/dependency_versions split — enough for GO-004's
// idempotency and restart-persistence needs; the relational split is
// deferred until a consumer actually needs to query across projects.
const resolutionsBucket = "project_dependencies"

// PutResolution upserts the resolution for projectID.
func (s *Store) PutResolution(ctx context.Context, projectID string, r domain.Resolution) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal resolution for %s: %w", projectID, err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(resolutionsBucket)).Put([]byte(projectID), data)
	})
}

// GetResolution returns the stored resolution for projectID, or ErrNotFound.
func (s *Store) GetResolution(ctx context.Context, projectID string) (domain.Resolution, error) {
	var r domain.Resolution
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(resolutionsBucket)).Get([]byte(projectID))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &r)
	})
	return r, err
}

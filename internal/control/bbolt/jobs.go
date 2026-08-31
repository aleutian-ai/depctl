package bbolt

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/domain"
)

const jobsBucket = "jobs"

// PutJob upserts a job record, keyed by its ID.
func (s *Store) PutJob(ctx context.Context, j domain.Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", j.ID, err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).Put([]byte(j.ID), data)
	})
}

// GetJob returns the job with the given ID, or ErrNotFound.
func (s *Store) GetJob(ctx context.Context, id string) (domain.Job, error) {
	var j domain.Job
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(jobsBucket)).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &j)
	})
	return j, err
}

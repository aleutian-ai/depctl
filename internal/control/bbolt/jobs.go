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

// ListJobs returns every job record, in key order.
func (s *Store) ListJobs(ctx context.Context) ([]domain.Job, error) {
	var jobs []domain.Job
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(jobsBucket)).ForEach(func(k, v []byte) error {
			var j domain.Job
			if err := json.Unmarshal(v, &j); err != nil {
				return fmt.Errorf("unmarshal job %s: %w", k, err)
			}
			jobs = append(jobs, j)
			return nil
		})
	})
	return jobs, err
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

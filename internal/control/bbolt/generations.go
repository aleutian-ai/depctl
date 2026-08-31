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

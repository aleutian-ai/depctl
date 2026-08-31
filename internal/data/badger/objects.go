package badger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	bg "github.com/dgraph-io/badger/v4"

	"aleutian-ai/ragctl/internal/domain"
)

const objectKeyPrefix = "obj/"

func objectKey(id string) []byte {
	return []byte(objectKeyPrefix + id)
}

// PutKnowledgeObject upserts a knowledge object, keyed by its ID.
func (s *Store) PutKnowledgeObject(ctx context.Context, obj domain.KnowledgeObject) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal knowledge object %s: %w", obj.ID, err)
	}
	return s.db.Update(func(txn *bg.Txn) error {
		return txn.Set(objectKey(obj.ID), data)
	})
}

// GetKnowledgeObject returns the knowledge object with the given ID, or
// ErrNotFound.
func (s *Store) GetKnowledgeObject(ctx context.Context, id string) (domain.KnowledgeObject, error) {
	var obj domain.KnowledgeObject
	err := s.db.View(func(txn *bg.Txn) error {
		item, err := txn.Get(objectKey(id))
		if errors.Is(err, bg.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &obj)
		})
	})
	return obj, err
}

// DeleteKnowledgeObject removes the knowledge object with the given ID.
func (s *Store) DeleteKnowledgeObject(ctx context.Context, id string) error {
	return s.db.Update(func(txn *bg.Txn) error {
		return txn.Delete(objectKey(id))
	})
}

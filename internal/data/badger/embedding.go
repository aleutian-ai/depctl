package badger

import (
	"context"
	"errors"

	bg "github.com/dgraph-io/badger/v4"
)

const embedKeyPrefix = "embed/"

func embedKey(key string) []byte {
	return []byte(embedKeyPrefix + key)
}

// PutEmbeddingMetadata upserts raw embedding metadata bytes under key
// (caller-constructed, e.g. "<generation-id>/<provider>/<chunk-id>").
func (s *Store) PutEmbeddingMetadata(ctx context.Context, key string, meta []byte) error {
	return s.db.Update(func(txn *bg.Txn) error {
		return txn.Set(embedKey(key), meta)
	})
}

// GetEmbeddingMetadata returns the raw embedding metadata bytes stored
// under key, or ErrNotFound.
func (s *Store) GetEmbeddingMetadata(ctx context.Context, key string) ([]byte, error) {
	var meta []byte
	err := s.db.View(func(txn *bg.Txn) error {
		item, err := txn.Get(embedKey(key))
		if errors.Is(err, bg.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		meta, err = item.ValueCopy(nil)
		return err
	})
	return meta, err
}

package badger

import (
	"context"
	"errors"

	bg "github.com/dgraph-io/badger/v4"
)

const hashKeyPrefix = "hash/"

func hashKey(contentHash string) []byte {
	return []byte(hashKeyPrefix + contentHash)
}

// PutContentHashIndex records that contentHash's content is stored under
// objectID, so a later object with identical content can be reused
// instead of duplicated (GEN-003).
func (s *Store) PutContentHashIndex(ctx context.Context, contentHash, objectID string) error {
	return s.db.Update(func(txn *bg.Txn) error {
		return txn.Set(hashKey(contentHash), []byte(objectID))
	})
}

// GetContentHashIndex returns the object ID previously stored for
// contentHash, or ErrNotFound if no object has that content yet.
func (s *Store) GetContentHashIndex(ctx context.Context, contentHash string) (string, error) {
	var objectID string
	err := s.db.View(func(txn *bg.Txn) error {
		item, err := txn.Get(hashKey(contentHash))
		if errors.Is(err, bg.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		objectID = string(val)
		return nil
	})
	return objectID, err
}

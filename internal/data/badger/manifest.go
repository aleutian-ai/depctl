package badger

import (
	"context"
	"errors"

	bg "github.com/dgraph-io/badger/v4"
)

const manifestKeyPrefix = "manifest/"

func manifestKey(generationID string) []byte {
	return []byte(manifestKeyPrefix + generationID)
}

// PutManifest upserts the raw manifest bytes for generationID.
func (s *Store) PutManifest(ctx context.Context, generationID string, manifest []byte) error {
	return s.db.Update(func(txn *bg.Txn) error {
		return txn.Set(manifestKey(generationID), manifest)
	})
}

// GetManifest returns the raw manifest bytes for generationID, or
// ErrNotFound.
func (s *Store) GetManifest(ctx context.Context, generationID string) ([]byte, error) {
	var manifest []byte
	err := s.db.View(func(txn *bg.Txn) error {
		item, err := txn.Get(manifestKey(generationID))
		if errors.Is(err, bg.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		manifest, err = item.ValueCopy(nil)
		return err
	})
	return manifest, err
}

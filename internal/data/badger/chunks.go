package badger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	bg "github.com/dgraph-io/badger/v4"

	"aleutian-ai/ragctl/internal/domain"
)

const chunkKeyPrefix = "chunk/"

func chunkKey(generationID, chunkID string) []byte {
	return []byte(chunkKeyPrefix + generationID + "/" + chunkID)
}

func chunkGenerationPrefix(generationID string) []byte {
	return []byte(chunkKeyPrefix + generationID + "/")
}

// PutChunk upserts a chunk, keyed by its generation and chunk ID.
func (s *Store) PutChunk(ctx context.Context, generationID string, c domain.Chunk) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal chunk %s: %w", c.ID, err)
	}
	return s.db.Update(func(txn *bg.Txn) error {
		return txn.Set(chunkKey(generationID, c.ID), data)
	})
}

// GetChunk returns the chunk with the given generation and chunk ID, or
// ErrNotFound.
func (s *Store) GetChunk(ctx context.Context, generationID, chunkID string) (domain.Chunk, error) {
	var c domain.Chunk
	err := s.db.View(func(txn *bg.Txn) error {
		item, err := txn.Get(chunkKey(generationID, chunkID))
		if errors.Is(err, bg.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &c)
		})
	})
	return c, err
}

// ListGenerationChunks returns every chunk staged for generationID.
func (s *Store) ListGenerationChunks(ctx context.Context, generationID string) ([]domain.Chunk, error) {
	var chunks []domain.Chunk
	prefix := chunkGenerationPrefix(generationID)
	err := s.db.View(func(txn *bg.Txn) error {
		opts := bg.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var c domain.Chunk
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &c)
			}); err != nil {
				return fmt.Errorf("unmarshal chunk %s: %w", it.Item().Key(), err)
			}
			chunks = append(chunks, c)
		}
		return nil
	})
	return chunks, err
}

// DeleteGeneration deletes every chunk and the manifest belonging to
// generationID, batched.
func (s *Store) DeleteGeneration(ctx context.Context, generationID string) error {
	wb := s.db.NewWriteBatch()
	defer wb.Cancel()

	prefix := chunkGenerationPrefix(generationID)
	err := s.db.View(func(txn *bg.Txn) error {
		opts := bg.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := it.Item().KeyCopy(nil)
			if err := wb.Delete(key); err != nil {
				return fmt.Errorf("batch delete %s: %w", key, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if err := wb.Delete(manifestKey(generationID)); err != nil {
		return fmt.Errorf("batch delete manifest for %s: %w", generationID, err)
	}
	return wb.Flush()
}

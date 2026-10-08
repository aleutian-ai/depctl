package badger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	bg "github.com/dgraph-io/badger/v4"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const chunkKeyPrefix = "chunk/"

func chunkKey(generationID, chunkID string) []byte {
	return []byte(chunkKeyPrefix + generationID + "/" + chunkID)
}

func chunkGenerationPrefix(generationID string) []byte {
	return []byte(chunkKeyPrefix + generationID + "/")
}

// storedChunk is a chunk's on-disk record. When a chunk's text is
// byte-identical to its parent object's (every symbol chunk, and any
// single-section document), ContentInObject is set and Content is left
// out: the text is stored once, on the object, and restored on read.
type storedChunk struct {
	domain.Chunk
	ContentInObject bool `json:",omitempty"`
}

func encodeChunk(c domain.Chunk, objectContent []byte) ([]byte, error) {
	rec := storedChunk{Chunk: c}
	if objectContent != nil && bytes.Equal(c.Content, objectContent) {
		rec.Content = nil
		rec.ContentInObject = true
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("marshal chunk %s: %w", c.ID, err)
	}
	return data, nil
}

// decodeChunk parses a chunk record and, if its text lives on the parent
// object, loads it within the same transaction.
func decodeChunk(txn *bg.Txn, data []byte) (domain.Chunk, error) {
	var rec storedChunk
	if err := json.Unmarshal(data, &rec); err != nil {
		return domain.Chunk{}, err
	}
	if rec.ContentInObject {
		obj, err := getObject(txn, rec.ObjectID)
		if err != nil {
			return domain.Chunk{}, fmt.Errorf("chunk %s: parent object %s: %w", rec.ID, rec.ObjectID, err)
		}
		rec.Content = obj.Content
	}
	return rec.Chunk, nil
}

// PutChunk upserts a chunk with its text stored inline, keyed by its
// generation and chunk ID.
func (s *Store) PutChunk(ctx context.Context, generationID string, c domain.Chunk) error {
	data, err := encodeChunk(c, nil)
	if err != nil {
		return err
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
			var derr error
			c, derr = decodeChunk(txn, val)
			return derr
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
				var derr error
				c, derr = decodeChunk(txn, val)
				return derr
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

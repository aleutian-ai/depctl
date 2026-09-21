package badger

import (
	"encoding/json"
	"fmt"

	bg "github.com/dgraph-io/badger/v4"

	"aleutian-ai/ragctl/internal/domain"
)

// Batch groups many writes into a few large commits instead of one
// commit per value. Writes are not visible to reads until Flush.
type Batch struct {
	wb *bg.WriteBatch
}

// NewBatch starts a batch. Call Flush to commit it; Cancel (safe to defer
// even after Flush) releases it if Flush is never reached.
func (s *Store) NewBatch() *Batch {
	return &Batch{wb: s.db.NewWriteBatch()}
}

// PutKnowledgeObject queues an object upsert, keyed by its ID.
func (b *Batch) PutKnowledgeObject(obj domain.KnowledgeObject) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal knowledge object %s: %w", obj.ID, err)
	}
	return b.wb.Set(objectKey(obj.ID), data)
}

// PutContentHashIndex queues a content-hash to object-ID index entry.
func (b *Batch) PutContentHashIndex(contentHash, objectID string) error {
	return b.wb.Set(hashKey(contentHash), []byte(objectID))
}

// PutChunk queues a chunk upsert. objectContent is the parent object's
// text: when the chunk's text is identical, it is stored once, on the
// object, and restored on read.
func (b *Batch) PutChunk(generationID string, c domain.Chunk, objectContent []byte) error {
	data, err := encodeChunk(c, objectContent)
	if err != nil {
		return err
	}
	return b.wb.Set(chunkKey(generationID, c.ID), data)
}

// PutEmbeddingMetadata queues an embedding-metadata upsert under key.
func (b *Batch) PutEmbeddingMetadata(key string, meta []byte) error {
	return b.wb.Set(embedKey(key), meta)
}

// Flush commits every queued write.
func (b *Batch) Flush() error { return b.wb.Flush() }

// Cancel discards anything not yet committed.
func (b *Batch) Cancel() { b.wb.Cancel() }

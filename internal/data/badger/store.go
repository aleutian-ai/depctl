// Package badger is ragctl's data-plane store: high-volume normalized
// content (knowledge objects, chunks, manifests, embedding metadata).
//
// This is the minimal slice needed for `ragctl init` to create the badger/
// directory. CRUD methods (PutKnowledgeObject, PutChunk, ...) are added as
// later commands need them — see STORE-003 in
// docs/tickets/completed/02-core-domain-storage.
package badger

import (
	"errors"
	"fmt"

	bg "github.com/dgraph-io/badger/v4"
)

// Badger tuning. Every value here is large (chunk text, whole objects,
// embedding vectors), and Badger's default keeps values under 1 MB inside
// the sorted tree, where each compaction rewrites them level after level:
// a live sync measured 1.2 GB of table-builder memory and a later OOM at
// 7.5 GB. A small threshold moves the bulk into the append-only value log
// so the tree holds only keys and pointers; the smaller memtables, cache
// and compactor count bound what remains. Values in the log are reclaimed
// by ReclaimValueLog, not by compaction.
const (
	valueThreshold = 1 << 10
	memTableSize   = 32 << 20
	numMemtables   = 2
	numCompactors  = 2
	blockCacheSize = 128 << 20
)

type Store struct {
	db *bg.DB
}

// Open opens (creating if necessary) the Badger database directory at path.
// Badger's own logging is silenced — it's noisy by default and not useful
// for a CLI tool.
func Open(path string) (*Store, error) {
	opts := bg.DefaultOptions(path).
		WithLogger(nil).
		WithValueThreshold(valueThreshold).
		WithMemTableSize(memTableSize).
		WithNumMemtables(numMemtables).
		WithNumCompactors(numCompactors).
		WithBlockCacheSize(blockCacheSize)
	db, err := bg.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open badger store %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// ReclaimValueLog rewrites value-log files that are mostly stale, so the
// disk space of deleted generations is returned. Call it after deleting
// data; it is a no-op when nothing is worth rewriting.
func (s *Store) ReclaimValueLog() error {
	for {
		err := s.db.RunValueLogGC(0.5)
		if errors.Is(err, bg.ErrNoRewrite) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("value log gc: %w", err)
		}
	}
}

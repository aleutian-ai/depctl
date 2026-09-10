// Package badger is ragctl's data-plane store: high-volume normalized
// content (knowledge objects, chunks, manifests, embedding metadata).
//
// This is the minimal slice needed for `ragctl init` to create the badger/
// directory. CRUD methods (PutKnowledgeObject, PutChunk, ...) are added as
// later commands need them — see STORE-003 in
// docs/tickets/completed/02-core-domain-storage.
package badger

import (
	"fmt"

	bg "github.com/dgraph-io/badger/v4"
)

type Store struct {
	db *bg.DB
}

// Open opens (creating if necessary) the Badger database directory at path.
// Badger's own logging is silenced — it's noisy by default and not useful
// for a CLI tool.
func Open(path string) (*Store, error) {
	opts := bg.DefaultOptions(path).WithLogger(nil)
	db, err := bg.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open badger store %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Package bbolt is ragctl's control-plane store: small, strongly
// structured state (projects, generations, jobs, references, ...).
//
// This is the minimal slice needed for `ragctl init` to create the
// control.db file with its buckets. CRUD methods (PutProject, PutGeneration,
// SetActiveGeneration, ...) are added as later commands need them — see
// STORE-001 in docs/tickets/planned/02-core-domain-storage.
package bbolt

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// buckets is the full bucket list from STORE-001, created up front so every
// later ticket can assume they exist.
var buckets = []string{
	"meta",
	"projects",
	"project_dependencies",
	"dependency_versions",
	"knowledge_sources",
	"generations",
	"active_generations",
	"backend_replicas",
	"jobs",
	"references",
	"retention",
	"migrations",
}

type Store struct {
	db *bolt.DB
}

// Open opens (creating if necessary) the bbolt database at path and
// ensures every control-plane bucket exists.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt store %s: %w", path, err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

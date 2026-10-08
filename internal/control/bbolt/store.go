// Package bbolt is depctl's control-plane store (control.db): small,
// strongly structured state such as projects, resolutions, generations,
// active-generation pointers, jobs and references.
package bbolt

import (
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// lockTimeout bounds how long Open waits for control.db's file lock.
// bbolt's default is to wait forever, which would turn any direct open
// while the daemon holds the lock into a silent hang. A var only so tests
// can shorten it.
var lockTimeout = 2 * time.Second

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
	noSourceVersionsBucket,
}

// Store is an open control.db.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if necessary) the bbolt database at path and
// ensures every control-plane bucket exists.
func Open(path string) (*Store, error) {
	return OpenWithTimeout(path, lockTimeout)
}

// OpenWithTimeout is Open with an explicit file-lock wait, for callers
// that want to fail faster (or slower) than the default: `depctl daemon
// run` racing another auto-started candidate for the same control.db
// wants near-zero, so a losing candidate exits before the winner could
// plausibly have already been asked to shut down (see internal/cli's
// spawnDaemonOnce).
func OpenWithTimeout(path string, timeout time.Duration) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, fmt.Errorf("open bbolt store %s: %w", path, ErrLocked)
	}
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

	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close closes the database, releasing its file lock.
func (s *Store) Close() error {
	return s.db.Close()
}

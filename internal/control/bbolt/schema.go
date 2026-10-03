package bbolt

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	bolt "go.etcd.io/bbolt"
)

// CurrentSchemaVersion is the bbolt control-plane schema version this
// binary understands. Bump it and append a Migration when a change needs
// one.
const CurrentSchemaVersion = 2

const schemaVersionKey = "schema_version"

// ErrUnsupportedSchemaVersion means the database on disk was written by a
// newer ragctl binary than this one. Open refuses to proceed rather than
// silently reinterpreting data it might not understand.
var ErrUnsupportedSchemaVersion = errors.New("unsupported schema version: database was written by a newer ragctl binary")

// Migration applies one schema change, keyed by the version it upgrades
// the database to.
type Migration struct {
	Version int
	Apply   func(tx *bolt.Tx) error
}

// migrations is a flat ordered slice, not a generic pluggable migration
// engine. Version 1's Apply is a no-op: every bucket it would create
// already exists by the time ensureSchema runs (Open creates them first),
// so it only establishes schema_version = 1 on a fresh database. Version
// 2 rekeys active-generation pointers by version (ADR-012, STORE-005).
var migrations = []Migration{
	{Version: 1, Apply: func(tx *bolt.Tx) error { return nil }},
	{Version: 2, Apply: migrateActivePointersPerVersion},
}

// ensureSchema reads the on-disk schema version (0 for a fresh database,
// meaning every migration is pending) and applies pending migrations in
// order, persisting the new version after each one succeeds. Called once
// by Open, before the store is returned to a caller. A migration
// returning an error aborts inside the same transaction, so a partial
// migration is never persisted.
func ensureSchema(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		current, err := readSchemaVersion(meta)
		if err != nil {
			return err
		}
		if current > CurrentSchemaVersion {
			return fmt.Errorf("%w: on-disk version %d, this binary supports up to %d", ErrUnsupportedSchemaVersion, current, CurrentSchemaVersion)
		}
		for _, m := range migrations {
			if m.Version <= current {
				continue
			}
			if err := m.Apply(tx); err != nil {
				return fmt.Errorf("apply schema migration %d: %w", m.Version, err)
			}
			if err := writeSchemaVersion(meta, m.Version); err != nil {
				return err
			}
		}
		return nil
	})
}

func readSchemaVersion(meta *bolt.Bucket) (int, error) {
	raw := meta.Get([]byte(schemaVersionKey))
	if raw == nil {
		return 0, nil
	}
	v, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("parse stored schema version %q: %w", raw, err)
	}
	return v, nil
}

func writeSchemaVersion(meta *bolt.Bucket, version int) error {
	return meta.Put([]byte(schemaVersionKey), []byte(strconv.Itoa(version)))
}

// SchemaVersion returns the database's current schema version, for
// `ragctl doctor` (OPS-002) to surface later.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		v, err = readSchemaVersion(tx.Bucket([]byte("meta")))
		return err
	})
	return v, err
}

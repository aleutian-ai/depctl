package bbolt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestOpenFreshDBWritesCurrentSchemaVersion(t *testing.T) {
	s := openTestStore(t)

	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", v, CurrentSchemaVersion)
	}
}

func TestOpenRefusesFutureSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")

	// Write a schema_version ahead of what this binary understands
	// directly via the raw bbolt handle, simulating a database left
	// behind by a newer depctl binary.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists([]byte("meta"))
		if err != nil {
			return err
		}
		return writeSchemaVersion(meta, CurrentSchemaVersion+98)
	})
	if err != nil {
		t.Fatalf("seed future schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded db: %v", err)
	}

	_, err = Open(path)
	if !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Fatalf("Open() error = %v, want ErrUnsupportedSchemaVersion", err)
	}
}

func TestEnsureSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v1, err := s1.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion (first): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	// Reopening an already-migrated database must be a no-op: same
	// version, no error, nothing re-applied.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	v2, err := s2.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion (second): %v", err)
	}
	if v1 != v2 {
		t.Fatalf("schema version changed across reopen: %d -> %d", v1, v2)
	}
	if v2 != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", v2, CurrentSchemaVersion)
	}
}

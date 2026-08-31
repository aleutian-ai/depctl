package bbolt

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/domain"
)

const backendReplicasBucket = "backend_replicas"

func backendReplicaKey(generationID, backendName string) []byte {
	return []byte(generationID + "/" + backendName)
}

// PutBackendReplica upserts a replica record, keyed by generation ID +
// backend name.
func (s *Store) PutBackendReplica(ctx context.Context, r domain.BackendReplica) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal backend replica %s/%s: %w", r.GenerationID, r.BackendName, err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(backendReplicasBucket)).Put(backendReplicaKey(r.GenerationID, r.BackendName), data)
	})
}

// GetBackendReplica returns the replica record for (generationID,
// backendName), or ErrNotFound.
func (s *Store) GetBackendReplica(ctx context.Context, generationID, backendName string) (domain.BackendReplica, error) {
	var r domain.BackendReplica
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(backendReplicasBucket)).Get(backendReplicaKey(generationID, backendName))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &r)
	})
	return r, err
}

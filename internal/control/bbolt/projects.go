package bbolt

import (
	"context"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const projectsBucket = "projects"

// PutProject upserts a project record, keyed by its ID.
func (s *Store) PutProject(ctx context.Context, p domain.Project) error {
	if err := p.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project %s: %w", p.ID, err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(projectsBucket)).Put([]byte(p.ID), data)
	})
}

// GetProject returns the project with the given ID, or ErrNotFound.
func (s *Store) GetProject(ctx context.Context, id string) (domain.Project, error) {
	var p domain.Project
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(projectsBucket)).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &p)
	})
	return p, err
}

// ListProjects returns every registered project.
func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	var projects []domain.Project
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(projectsBucket)).ForEach(func(k, v []byte) error {
			var p domain.Project
			if err := json.Unmarshal(v, &p); err != nil {
				return fmt.Errorf("unmarshal project %s: %w", k, err)
			}
			projects = append(projects, p)
			return nil
		})
	})
	return projects, err
}

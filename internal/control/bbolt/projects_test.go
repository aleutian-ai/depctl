package bbolt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPutGetProject(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := domain.Project{ID: "proj_abc", Root: "/tmp/foo", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.PutProject(ctx, p); err != nil {
		t.Fatalf("PutProject: %v", err)
	}

	got, err := s.GetProject(ctx, "proj_abc")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Root != p.Root {
		t.Errorf("Root = %q, want %q", got.Root, p.Root)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.GetProject(context.Background(), "proj_missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListProjects(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"proj_a", "proj_b", "proj_c"} {
		if err := s.PutProject(ctx, domain.Project{ID: id, Root: "/tmp/" + id}); err != nil {
			t.Fatalf("PutProject %s: %v", id, err)
		}
	}

	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d projects, want 3", len(list))
	}
}

func TestProjectPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	ctx := context.Background()

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s1.PutProject(ctx, domain.Project{ID: "proj_restart", Root: "/tmp/restart"}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetProject(ctx, "proj_restart")
	if err != nil {
		t.Fatalf("GetProject after restart: %v", err)
	}
	if got.Root != "/tmp/restart" {
		t.Errorf("Root = %q after restart, want /tmp/restart", got.Root)
	}
}

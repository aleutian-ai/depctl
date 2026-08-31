package bbolt

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestPutGetResolution(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	r := domain.Resolution{
		Ecosystem: domain.EcosystemGo,
		Dependencies: []domain.DependencyVersion{
			{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/dep"}, Version: "v1.0.0"},
		},
		Fingerprint: "res_abc",
	}
	if err := s.PutResolution(ctx, "proj_abc", r); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	got, err := s.GetResolution(ctx, "proj_abc")
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("GetResolution = %+v, want %+v", got, r)
	}
}

func TestGetResolutionNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.GetResolution(context.Background(), "proj_missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolutionPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	ctx := context.Background()

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r := domain.Resolution{Ecosystem: domain.EcosystemGo, Fingerprint: "res_restart"}
	if err := s1.PutResolution(ctx, "proj_restart", r); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetResolution(ctx, "proj_restart")
	if err != nil {
		t.Fatalf("GetResolution after restart: %v", err)
	}
	if got.Fingerprint != "res_restart" {
		t.Errorf("Fingerprint = %q after restart, want res_restart", got.Fingerprint)
	}
}

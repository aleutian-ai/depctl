package bbolt

import (
	"context"
	"errors"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestGetJobNotFound(t *testing.T) {
	store := openTestStore(t)
	_, err := store.GetJob(context.Background(), "job_missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJob = %v, want ErrNotFound", err)
	}
}

func TestPutGetJob(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	job := domain.Job{ID: "job_1", Type: "gc", State: domain.JobPending}
	if err := store.PutJob(ctx, job); err != nil {
		t.Fatalf("PutJob: %v", err)
	}

	got, err := store.GetJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != domain.JobPending || got.Type != "gc" {
		t.Errorf("GetJob = %+v, want Type=gc State=PENDING", got)
	}
}

func TestJobPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/control.db"

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.PutJob(ctx, domain.Job{ID: "job_1", Type: "gc", State: domain.JobRunning}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("GetJob after reopen: %v", err)
	}
	if got.State != domain.JobRunning {
		t.Errorf("State after reopen = %s, want RUNNING", got.State)
	}
}

func TestListJobs(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	for _, j := range []domain.Job{
		{ID: "job_a", Type: "gc", State: domain.JobRunning},
		{ID: "job_b", Type: "gc", State: domain.JobFailed},
	} {
		if err := store.PutJob(ctx, j); err != nil {
			t.Fatalf("PutJob %s: %v", j.ID, err)
		}
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != "job_a" || jobs[1].ID != "job_b" {
		t.Errorf("ListJobs = %+v, want job_a then job_b", jobs)
	}
}

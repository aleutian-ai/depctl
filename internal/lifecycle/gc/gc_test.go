package gc

import (
	"context"
	"errors"
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/backendtest"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/retention"
)

// fakeControlStore is a minimal in-memory ControlStore/DataStore pair
// for testing Run's job claim/resume/failure orchestration in isolation
// from real bbolt/Badger — those stores' own semantics are already
// covered by internal/control/bbolt's tests.
type fakeControlStore struct {
	jobs             map[string]domain.Job
	generations      map[string]domain.Generation // by generation ID
	deletedGenIDs    map[string]bool
	deletedRefs      map[string]bool // key: eco|pkg|version
	clearedActive    map[string]bool // key: eco|pkg|version|backend
	failListGens     bool
	failDeleteRecord bool
}

func newFakeControlStore() *fakeControlStore {
	return &fakeControlStore{
		jobs:          map[string]domain.Job{},
		generations:   map[string]domain.Generation{},
		deletedGenIDs: map[string]bool{},
		deletedRefs:   map[string]bool{},
		clearedActive: map[string]bool{},
	}
}

func (s *fakeControlStore) GetJob(ctx context.Context, id string) (domain.Job, error) {
	j, ok := s.jobs[id]
	if !ok {
		return domain.Job{}, errors.New("not found")
	}
	return j, nil
}

func (s *fakeControlStore) PutJob(ctx context.Context, j domain.Job) error {
	s.jobs[j.ID] = j
	return nil
}

func (s *fakeControlStore) ListGenerationsByDependencyVersion(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.Generation, error) {
	if s.failListGens {
		return nil, errors.New("simulated list failure")
	}
	var out []domain.Generation
	for _, g := range s.generations {
		if g.Dependency.Dependency.Ecosystem == ecosystem && g.Dependency.Dependency.Name == pkg && g.Dependency.Version == version {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *fakeControlStore) DeleteGenerationRecord(ctx context.Context, id string) error {
	if s.failDeleteRecord {
		return errors.New("simulated delete-record failure")
	}
	delete(s.generations, id)
	s.deletedGenIDs[id] = true
	return nil
}

func (s *fakeControlStore) DeleteAllReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) error {
	s.deletedRefs[string(ecosystem)+"|"+pkg+"|"+version] = true
	return nil
}

func (s *fakeControlStore) ClearActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, backendName string) error {
	s.clearedActive[string(ecosystem)+"|"+pkg+"|"+version+"|"+backendName] = true
	return nil
}

type fakeDataStore struct {
	deletedGenIDs map[string]bool
	failDelete    bool
}

func newFakeDataStore() *fakeDataStore {
	return &fakeDataStore{deletedGenIDs: map[string]bool{}}
}

func (s *fakeDataStore) DeleteGeneration(ctx context.Context, generationID string) error {
	if s.failDelete {
		return errors.New("simulated badger delete failure")
	}
	s.deletedGenIDs[generationID] = true
	return nil
}

func testCandidate() retention.GCCandidate {
	return retention.GCCandidate{Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.60.0", Reason: "grace_expired"}
}

func TestRunDeletesInOrderAndMarksJobSucceeded(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	control.generations["gen_1"] = domain.Generation{
		ID: "gen_1",
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
			Version:    "v1.60.0",
		},
	}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	if err := vb.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	results, err := Run(ctx, control, data, vb, ns, []retention.GCCandidate{testCandidate()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results = %+v, want one succeeded result", results)
	}

	if !data.deletedGenIDs["gen_1"] {
		t.Error("Badger DeleteGeneration was never called for gen_1")
	}
	if !control.deletedGenIDs["gen_1"] {
		t.Error("bbolt DeleteGenerationRecord was never called for gen_1")
	}
	if !control.deletedRefs["go|google.golang.org/grpc|v1.60.0"] {
		t.Error("DeleteAllReferences was never called")
	}
	// ADR-012: GC retires the version's own active pointer, for exactly
	// that version.
	if !control.clearedActive["go|google.golang.org/grpc|v1.60.0|"+vb.Name()] {
		t.Errorf("ClearActiveGeneration was never called for v1.60.0 (cleared: %v)", control.clearedActive)
	}

	jobID := JobID("gc", domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
		Version:    "v1.60.0",
	})
	job, err := control.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != domain.JobSucceeded {
		t.Errorf("job.State = %s, want SUCCEEDED", job.State)
	}
}

func TestRunFailureMarksJobFailedAndDoesNotBlockOtherCandidates(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	control.failListGens = true // makes the first candidate's deletion fail
	data := newFakeDataStore()
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)

	candidates := []retention.GCCandidate{
		testCandidate(),
		{Ecosystem: domain.EcosystemGo, Package: "github.com/pkg/errors", Version: "v0.9.1", Reason: "grace_expired"},
	}

	// Only fail listing for the grpc candidate; let the second succeed.
	results := make([]Result, 0, 2)
	r1, err := Run(ctx, control, data, vb, ns, candidates[:1])
	if err != nil {
		t.Fatalf("Run (candidate 1): %v", err)
	}
	results = append(results, r1...)

	control.failListGens = false
	r2, err := Run(ctx, control, data, vb, ns, candidates[1:])
	if err != nil {
		t.Fatalf("Run (candidate 2): %v", err)
	}
	results = append(results, r2...)

	if results[0].Succeeded {
		t.Error("first candidate should have failed")
	}
	if results[0].Error == "" {
		t.Error("first candidate's Result.Error is empty, want the failure reason")
	}
	if !results[1].Succeeded {
		t.Errorf("second candidate should have succeeded independently: %+v", results[1])
	}

	jobID := JobID("gc", domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
		Version:    "v1.60.0",
	})
	job, err := control.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != domain.JobFailed {
		t.Errorf("job.State = %s, want FAILED", job.State)
	}
	if job.LastError == "" {
		t.Error("job.LastError is empty, want the failure reason recorded")
	}
}

func TestRunResumesAfterPartialFailure(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	control.generations["gen_1"] = domain.Generation{
		ID: "gen_1",
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
			Version:    "v1.60.0",
		},
	}
	data := newFakeDataStore()
	data.failDelete = true // simulate crash/failure at the Badger-delete step
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)

	candidate := testCandidate()
	first, err := Run(ctx, control, data, vb, ns, []retention.GCCandidate{candidate})
	if err != nil {
		t.Fatalf("Run (first): %v", err)
	}
	if first[0].Succeeded {
		t.Fatal("first run should have failed at the Badger delete step")
	}
	if control.deletedGenIDs["gen_1"] {
		t.Error("bbolt record should NOT be deleted yet — Badger delete failed first")
	}

	// "Fix" the simulated failure and re-run — must complete cleanly
	// from where it left off (restartability).
	data.failDelete = false
	second, err := Run(ctx, control, data, vb, ns, []retention.GCCandidate{candidate})
	if err != nil {
		t.Fatalf("Run (second): %v", err)
	}
	if !second[0].Succeeded {
		t.Fatalf("second run should have succeeded: %+v", second[0])
	}
	if !data.deletedGenIDs["gen_1"] || !control.deletedGenIDs["gen_1"] {
		t.Error("resumed run did not complete the remaining deletion steps")
	}
}

func TestRunOnAlreadySucceededJobIsANoOp(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	data.failDelete = true // if Run tried to redo work, this would fail it
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)

	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
		Version:    "v1.60.0",
	}
	jobID := JobID("gc", dep)
	control.jobs[jobID] = domain.Job{ID: jobID, Type: "gc", Dependency: dep, State: domain.JobSucceeded}

	results, err := Run(ctx, control, data, vb, ns, []retention.GCCandidate{testCandidate()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !results[0].Succeeded {
		t.Errorf("re-running an already-succeeded job should report success without redoing work: %+v", results[0])
	}
}

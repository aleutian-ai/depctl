// Package gc implements RET-004: executing internal/retention's GC plans
// (unreferenced versions, orphan generations, superseded duplicates) by
// deleting data from the search index, Badger, and bbolt, in that fixed
// order, restartably and idempotently via bbolt's job system.
package gc

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/zeebo/blake3"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/retention"
)

// ControlStore is the narrow slice of *bbolt.Store this package needs.
type ControlStore interface {
	GetJob(ctx context.Context, id string) (domain.Job, error)
	PutJob(ctx context.Context, j domain.Job) error
	ListGenerationsByDependencyVersion(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.Generation, error)
	DeleteGenerationRecord(ctx context.Context, id string) error
	DeleteAllReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) error
	ClearActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, backendName string) error
}

// DataStore is the narrow slice of *badger.Store this package needs.
type DataStore interface {
	DeleteGeneration(ctx context.Context, generationID string) error
}

// JobID deterministically derives a job ID from its type and dependency
// version, so re-running GC against the same candidate finds (and
// resumes) the same job rather than creating a duplicate — the
// restartability RET-004 requires.
func JobID(jobType string, dep domain.DependencyVersion) string {
	h := blake3.Sum256([]byte(jobType + "|" + string(dep.Dependency.Ecosystem) + "|" + dep.Dependency.Name + "|" + dep.Version))
	return "job_" + hex.EncodeToString(h[:16])
}

// Result reports what happened for one GCCandidate.
type Result struct {
	Candidate retention.GCCandidate
	Succeeded bool
	Error     string
}

// Run executes every candidate in plan: claim/resume its job, clear the
// version's active pointer, delete its index points (vb is every index
// the install writes), its Badger generation data, then its bbolt generation
// and reference records, in that fixed order — then mark the job
// SUCCEEDED. A candidate's deletion failure marks its job FAILED with
// LastError and does not block the remaining candidates; each step is
// itself idempotent (deleting something already gone is a no-op), so
// re-running Run after a partial failure resumes cleanly from wherever
// it stopped.
func Run(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, plan []retention.GCCandidate) ([]Result, error) {
	results := make([]Result, 0, len(plan))
	for _, candidate := range plan {
		result := runOne(ctx, control, data, vb, ns, candidate)
		results = append(results, result)
	}
	return results, nil
}

func runOne(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.GCCandidate) Result {
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: candidate.Ecosystem, Name: candidate.Package},
		Version:    candidate.Version,
	}
	id := JobID("gc", dep)

	job, err := control.GetJob(ctx, id)
	now := time.Now()
	if err != nil {
		job = domain.Job{ID: id, Type: "gc", Dependency: dep, State: domain.JobPending, CreatedAt: now}
	}
	if job.State == domain.JobSucceeded {
		// Already fully deleted by a prior run — nothing left to do.
		return Result{Candidate: candidate, Succeeded: true}
	}

	job.State = domain.JobRunning
	job.UpdatedAt = now
	if err := control.PutJob(ctx, job); err != nil {
		return Result{Candidate: candidate, Error: fmt.Sprintf("claim job: %v", err)}
	}

	if err := deleteCandidate(ctx, control, data, vb, ns, candidate); err != nil {
		job.State = domain.JobFailed
		job.LastError = err.Error()
		job.UpdatedAt = time.Now()
		if putErr := control.PutJob(ctx, job); putErr != nil {
			// Job bookkeeping is advisory; the deletion failure itself
			// (already captured) is what callers need to see.
			fmt.Printf("gc: failed to persist FAILED job state for %s: %v\n", id, putErr)
		}
		return Result{Candidate: candidate, Error: err.Error()}
	}

	job.State = domain.JobSucceeded
	job.UpdatedAt = time.Now()
	if err := control.PutJob(ctx, job); err != nil {
		return Result{Candidate: candidate, Error: fmt.Sprintf("mark job succeeded: %v", err)}
	}
	return Result{Candidate: candidate, Succeeded: true}
}

// deleteCandidate retires the version's active pointer, then performs
// RET-004's three-step deletion in its fixed order: vector replica, then
// Badger, then bbolt metadata.
func deleteCandidate(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.GCCandidate) error {
	// Step 0 (ADR-012): stop serving the version before deleting it, so
	// search never resolves a pointer to data that's mid-deletion and no
	// dangling pointer is left behind.
	if err := control.ClearActiveGeneration(ctx, candidate.Ecosystem, candidate.Package, candidate.Version, vb.Name()); err != nil {
		return fmt.Errorf("clear active generation: %w", err)
	}

	// Step 1: vector replica, filtered by dependency+version.
	err := vb.Delete(ctx, backend.DeleteRequest{
		Namespace: ns.Name,
		Filter:    &backend.Filter{Ecosystem: string(candidate.Ecosystem), Dependency: candidate.Package, Version: candidate.Version},
	})
	if err != nil {
		return fmt.Errorf("delete vector replica: %w", err)
	}

	// Step 2: Badger generation/chunk objects, once per generation ID
	// this dependency+version ever produced.
	gens, err := control.ListGenerationsByDependencyVersion(ctx, candidate.Ecosystem, candidate.Package, candidate.Version)
	if err != nil {
		return fmt.Errorf("list generations: %w", err)
	}
	for _, g := range gens {
		if err := data.DeleteGeneration(ctx, g.ID); err != nil {
			return fmt.Errorf("delete badger generation %s: %w", g.ID, err)
		}
	}

	// Step 3: bbolt generation records + reference metadata.
	for _, g := range gens {
		if err := control.DeleteGenerationRecord(ctx, g.ID); err != nil {
			return fmt.Errorf("delete generation record %s: %w", g.ID, err)
		}
	}
	if err := control.DeleteAllReferences(ctx, candidate.Ecosystem, candidate.Package, candidate.Version); err != nil {
		return fmt.Errorf("delete reference metadata: %w", err)
	}

	return nil
}

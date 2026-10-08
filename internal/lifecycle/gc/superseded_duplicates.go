package gc

import (
	"context"
	"fmt"
	"time"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/retention"
)

// SupersededDuplicateResult reports what happened for one
// retention.SupersededDuplicateCandidate.
type SupersededDuplicateResult struct {
	Candidate retention.SupersededDuplicateCandidate
	Succeeded bool
	Error     string
}

// supersededDuplicateJobID derives a stable job ID directly from the
// generation ID, matching orphanJobID's own convention — a
// SupersededDuplicateCandidate's GenerationID is already a unique,
// stable identifier, unlike a GCCandidate's dependency+version tuple.
func supersededDuplicateJobID(generationID string) string {
	return "job_superseded_dup_" + generationID
}

// RunSupersededDuplicates executes every candidate in plan: claim/resume
// its job, delete its vector-backend points (filtered by Generation
// alone — a dependency+version-scoped filter, like Run uses, would also
// delete the ACTIVE generation's own identical-version points, which
// this must never touch), then its Badger generation data, then its
// bbolt generation record. Deliberately never touches reference
// records, same reasoning as RunOrphans: a SUPERSEDED generation that
// lost a promotion race never owned reference rows of its own — the
// version's reference rows belong to whichever generation is currently
// ACTIVE. Same fixed deletion order and idempotent-retry behavior as
// Run/RunOrphans.
func RunSupersededDuplicates(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, plan []retention.SupersededDuplicateCandidate) ([]SupersededDuplicateResult, error) {
	results := make([]SupersededDuplicateResult, 0, len(plan))
	for _, candidate := range plan {
		results = append(results, runOneSupersededDuplicate(ctx, control, data, vb, ns, candidate))
	}
	return results, nil
}

func runOneSupersededDuplicate(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.SupersededDuplicateCandidate) SupersededDuplicateResult {
	id := supersededDuplicateJobID(candidate.GenerationID)
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: candidate.Ecosystem, Name: candidate.Package},
		Version:    candidate.Version,
	}

	job, err := control.GetJob(ctx, id)
	now := time.Now()
	if err != nil {
		job = domain.Job{ID: id, Type: "gc_superseded_duplicate", Dependency: dep, State: domain.JobPending, CreatedAt: now}
	}
	if job.State == domain.JobSucceeded {
		// Already fully deleted by a prior run — nothing left to do.
		return SupersededDuplicateResult{Candidate: candidate, Succeeded: true}
	}

	job.State = domain.JobRunning
	job.UpdatedAt = now
	if err := control.PutJob(ctx, job); err != nil {
		return SupersededDuplicateResult{Candidate: candidate, Error: fmt.Sprintf("claim job: %v", err)}
	}

	if err := deleteSupersededDuplicate(ctx, control, data, vb, ns, candidate); err != nil {
		job.State = domain.JobFailed
		job.LastError = err.Error()
		job.UpdatedAt = time.Now()
		if putErr := control.PutJob(ctx, job); putErr != nil {
			// Job bookkeeping is advisory; the deletion failure itself
			// (already captured) is what callers need to see.
			fmt.Printf("gc: failed to persist FAILED job state for %s: %v\n", id, putErr)
		}
		return SupersededDuplicateResult{Candidate: candidate, Error: err.Error()}
	}

	job.State = domain.JobSucceeded
	job.UpdatedAt = time.Now()
	if err := control.PutJob(ctx, job); err != nil {
		return SupersededDuplicateResult{Candidate: candidate, Error: fmt.Sprintf("mark job succeeded: %v", err)}
	}
	return SupersededDuplicateResult{Candidate: candidate, Succeeded: true}
}

// deleteSupersededDuplicate performs the three-step deletion,
// generation-ID scoped throughout — no dependency+version-scoped step
// anywhere, and no DeleteAllReferences call (see RunSupersededDuplicates'
// own doc comment).
func deleteSupersededDuplicate(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.SupersededDuplicateCandidate) error {
	// Step 1: vector points for this exact generation only — reuses the
	// already-wired backend.Filter.Generation field, no interface change.
	if err := vb.Delete(ctx, backend.DeleteRequest{
		Namespace: ns.Name,
		Filter:    &backend.Filter{Generation: candidate.GenerationID},
	}); err != nil {
		return fmt.Errorf("delete vector replica for generation %s: %w", candidate.GenerationID, err)
	}

	// Step 2: Badger content for this generation.
	if err := data.DeleteGeneration(ctx, candidate.GenerationID); err != nil {
		return fmt.Errorf("delete badger generation %s: %w", candidate.GenerationID, err)
	}

	// Step 3: bbolt generation record.
	if err := control.DeleteGenerationRecord(ctx, candidate.GenerationID); err != nil {
		return fmt.Errorf("delete generation record %s: %w", candidate.GenerationID, err)
	}
	return nil
}

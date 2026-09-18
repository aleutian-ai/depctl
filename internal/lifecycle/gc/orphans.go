package gc

import (
	"context"
	"fmt"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/retention"
)

// OrphanResult reports what happened for one retention.OrphanCandidate.
type OrphanResult struct {
	Candidate retention.OrphanCandidate
	Succeeded bool
	Error     string
}

// orphanJobID derives a stable job ID directly from the generation ID —
// unlike JobID, which hashes a dependency+version because a GCCandidate
// has no ID of its own, an OrphanCandidate's GenerationID already is a
// unique, stable identifier.
func orphanJobID(generationID string) string {
	return "job_orphan_" + generationID
}

// RunOrphans executes every candidate in plan: claim/resume its job,
// delete its vector-backend points (filtered by Generation alone — a
// dependency+version-scoped filter, like Run uses, would be unsafe here
// since a healthy generation can share the same dependency+version as
// an orphan, e.g. a retry that succeeded after an earlier attempt
// failed), then its Badger generation data, then its bbolt generation
// record — deliberately never touching reference records, since an
// orphan generation was never promoted and so never owned any. Same
// fixed deletion order and same idempotent-retry behavior as Run.
func RunOrphans(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, plan []retention.OrphanCandidate) ([]OrphanResult, error) {
	results := make([]OrphanResult, 0, len(plan))
	for _, candidate := range plan {
		results = append(results, runOneOrphan(ctx, control, data, vb, ns, candidate))
	}
	return results, nil
}

func runOneOrphan(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.OrphanCandidate) OrphanResult {
	id := orphanJobID(candidate.GenerationID)
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: candidate.Ecosystem, Name: candidate.Package},
		Version:    candidate.Version,
	}

	job, err := control.GetJob(ctx, id)
	now := time.Now()
	if err != nil {
		job = domain.Job{ID: id, Type: "gc_orphan", Dependency: dep, State: domain.JobPending, CreatedAt: now}
	}
	if job.State == domain.JobSucceeded {
		// Already fully deleted by a prior run — nothing left to do.
		return OrphanResult{Candidate: candidate, Succeeded: true}
	}

	job.State = domain.JobRunning
	job.UpdatedAt = now
	if err := control.PutJob(ctx, job); err != nil {
		return OrphanResult{Candidate: candidate, Error: fmt.Sprintf("claim job: %v", err)}
	}

	if err := deleteOrphanCandidate(ctx, control, data, vb, ns, candidate); err != nil {
		job.State = domain.JobFailed
		job.LastError = err.Error()
		job.UpdatedAt = time.Now()
		if putErr := control.PutJob(ctx, job); putErr != nil {
			// Job bookkeeping is advisory; the deletion failure itself
			// (already captured) is what callers need to see.
			fmt.Printf("gc: failed to persist FAILED job state for %s: %v\n", id, putErr)
		}
		return OrphanResult{Candidate: candidate, Error: err.Error()}
	}

	job.State = domain.JobSucceeded
	job.UpdatedAt = time.Now()
	if err := control.PutJob(ctx, job); err != nil {
		return OrphanResult{Candidate: candidate, Error: fmt.Sprintf("mark job succeeded: %v", err)}
	}
	return OrphanResult{Candidate: candidate, Succeeded: true}
}

// deleteOrphanCandidate performs the three-step deletion, generation-ID
// scoped throughout — no dependency+version-scoped step anywhere, and
// no DeleteAllReferences call, since an orphan generation never owned
// reference rows in the first place.
func deleteOrphanCandidate(ctx context.Context, control ControlStore, data DataStore, vb backend.VectorBackend, ns backend.Namespace, candidate retention.OrphanCandidate) error {
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

	// Step 3: bbolt generation record. No DeleteAllReferences call — an
	// orphan generation was never promoted, so it never owned reference
	// rows; any reference rows for this dependency+version belong to a
	// different, actually-promoted generation, if one exists.
	if err := control.DeleteGenerationRecord(ctx, candidate.GenerationID); err != nil {
		return fmt.Errorf("delete generation record %s: %w", candidate.GenerationID, err)
	}
	return nil
}

package validate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/data/badger"
	"github.com/aleutian-ai/depctl/internal/data/generation"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/embedding"
	"github.com/aleutian-ai/depctl/internal/observability"
	"github.com/aleutian-ai/depctl/internal/observability/trace"
)

// Report bundles VAL-001..003's three independent results, since a
// caller (e.g. Promote) needs all three passed before promotion.
type Report struct {
	Structural         StructuralResult
	Sanity             StructuralResult
	VersionCorrectness StructuralResult
}

// Passed reports whether every check in the report passed.
func (r Report) Passed() bool {
	return r.Structural.Passed && r.Sanity.Passed && r.VersionCorrectness.Passed
}

// Failures flattens every check's failures into one list, each prefixed
// with which check reported it.
func (r Report) Failures() []string {
	var all []string
	for _, f := range r.Structural.Failures {
		all = append(all, "structural: "+f)
	}
	for _, f := range r.Sanity.Failures {
		all = append(all, "sanity: "+f)
	}
	for _, f := range r.VersionCorrectness.Failures {
		all = append(all, "version-correctness: "+f)
	}
	return all
}

// Run drives gen (which must have already been through Build then
// Replicate) through VAL-001..003 in sequence, transitioning its bbolt
// state VALIDATING -> {READY, FAILED} as GEN-002/CORE-002's state
// machine defines. No ticket specifies this orchestrator any more than
// GEN-002/VEC-003 specified generation.Replicate — it's built the same
// way, as validate's own lifecycle-advancing entry point, so VAL-004's
// Promote has a real READY candidate plus a real Report to consume
// rather than hand-assembled StructuralResults.
//
// prior/priorManifest are nil for a dependency's first-ever generation
// (Sanity auto-passes in that case, per VAL-002).
func Run(ctx context.Context, gen domain.Generation, manifest generation.Manifest, replica domain.BackendReplica, prior *domain.Generation, priorManifest *generation.Manifest, cfg SanityConfig, embedder *embedding.Prompted, vb backend.VectorBackend, ns backend.Namespace, store *bbolt.Store, badgerStore *badger.Store) (resultGen domain.Generation, resultReport Report, err error) {
	ctx, end := trace.StartSpan(ctx, "validate")
	defer func() {
		if err != nil {
			trace.RecordError(ctx, err)
		}
		end()
	}()
	validateStart := time.Now()
	gen.State = domain.GenValidating
	gen.UpdatedAt = time.Now()
	if err := store.PutGeneration(ctx, gen); err != nil {
		return gen, Report{}, fmt.Errorf("validate: persist VALIDATING state for %s: %w", gen.ID, err)
	}

	structural, err := Structural(ctx, gen, manifest, replica, badgerStore)
	if err != nil {
		return gen, Report{}, fmt.Errorf("validate: structural check: %w", err)
	}

	sanity, err := Sanity(ctx, gen, prior, manifest, priorManifest, cfg)
	if err != nil {
		return gen, Report{}, fmt.Errorf("validate: sanity check: %w", err)
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		return gen, Report{}, fmt.Errorf("validate: list chunks for %s: %w", gen.ID, err)
	}
	versionCorrectness, err := VersionCorrectness(ctx, embedder, vb, ns, gen, SampleChunks(chunks))
	if err != nil {
		return gen, Report{}, fmt.Errorf("validate: version-correctness check: %w", err)
	}

	report := Report{Structural: structural, Sanity: sanity, VersionCorrectness: versionCorrectness}

	if report.Passed() {
		gen.State = domain.GenReady
	} else {
		gen.State = domain.GenFailed
		gen.Error = strings.Join(report.Failures(), "; ")
	}
	gen.UpdatedAt = time.Now()
	if err := store.PutGeneration(ctx, gen); err != nil {
		return gen, report, fmt.Errorf("validate: persist final state for %s: %w", gen.ID, err)
	}

	observability.FromContext(ctx).Info("validate completed",
		observability.KeyStage, "validate",
		observability.KeyDependency, gen.Dependency.Dependency.Name,
		observability.KeyVersion, gen.Dependency.Version,
		observability.KeyGeneration, gen.ID,
		observability.KeyDurationMS, time.Since(validateStart).Milliseconds(),
		"passed", report.Passed(),
	)
	return gen, report, nil
}

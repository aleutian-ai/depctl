package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/api"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/lifecycle/gc"
	"aleutian-ai/ragctl/internal/observability"
	"aleutian-ai/ragctl/internal/observability/metrics"
	"aleutian-ai/ragctl/internal/observability/trace"
	"aleutian-ai/ragctl/internal/retention"
)

func newGCCmd() *cobra.Command {
	var dryRun, orphans, supersededDuplicates bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Garbage-collect unreferenced versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGC(cmd, dryRun, orphans, supersededDuplicates)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print GC candidates without deleting anything")
	cmd.Flags().BoolVar(&orphans, "orphans", false, "target FAILED/stuck generations instead of unreferenced versions (see GC-001)")
	cmd.Flags().BoolVar(&supersededDuplicates, "superseded-duplicates", false, "target SUPERSEDED generations left behind by a same-version build race, still sharing an ACTIVE sibling (see POINT-004)")
	return cmd
}

func runGC(cmd *cobra.Command, dryRun, orphans, supersededDuplicates bool) error {
	if orphans && supersededDuplicates {
		return fmt.Errorf("--orphans and --superseded-duplicates are separate GC passes and cannot be combined in one run")
	}
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	res, err := c.GC(cmd.Context(), dryRun, orphans, supersededDuplicates, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d GC candidate(s) failed", res.Failed)
	}
	return nil
}

// RunGC plans and executes garbage collection, writing its report to
// out. It runs inside the daemon, against the stores it holds open.
func RunGC(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, dryRun bool, out io.Writer, vecReadiness *vectorReadiness) (result api.GCResult, err error) {
	ctx, spanEnd := trace.StartSpan(ctx, "gc")
	gcStart := time.Now()
	// A defer, not a call at each return point, so every exit path —
	// zero candidates, dry-run, or a real deletion run — logs exactly
	// once with whatever result/err this call actually produced.
	defer func() {
		if err != nil {
			trace.RecordError(ctx, err)
		}
		spanEnd()
		logger := observability.FromContext(ctx)
		fields := []any{
			observability.KeyStage, "gc",
			observability.KeyBackend, cfg.Vector.Backend,
			observability.KeyDurationMS, time.Since(gcStart).Milliseconds(),
			"dry_run", dryRun, "candidates", result.Candidates, "deleted", result.Deleted, "failed", result.Failed,
		}
		if err != nil {
			logger.Error("gc failed", append(fields, "error", err)...)
		} else {
			logger.Info("gc completed", fields...)
		}
	}()

	candidates, err := retention.PlanGC(ctx, store, cfg.Vector.Backend, cfg.Retention.GracePeriod, time.Now())
	if err != nil {
		return api.GCResult{}, fmt.Errorf("plan GC: %w", err)
	}

	result = api.GCResult{Candidates: len(candidates), DryRun: dryRun}
	metrics.GCCandidates.Set(float64(len(candidates)))
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing eligible for garbage collection")
		return result, nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-8s %-45s %-15s %s\n", c.Ecosystem, c.Package, c.Version, c.Reason)
	}

	if dryRun {
		return result, nil
	}

	if usesEmbedder(cfg) {
		if err := vecReadiness.checkReady(); err != nil {
			return result, err
		}
	}
	vb, err := buildAllIndexes(cfg)
	if err != nil {
		return result, err
	}
	ns := backend.Namespace{Name: cfg.Vector.Collection}

	results, err := gc.Run(ctx, store, badgerStore, vb, ns, candidates)
	if err != nil {
		return result, fmt.Errorf("run GC: %w", err)
	}

	for _, r := range results {
		if r.Succeeded {
			result.Deleted++
			fmt.Fprintf(out, "OK    %s %s\n", r.Candidate.Package, r.Candidate.Version)
		} else {
			result.Failed++
			fmt.Fprintf(out, "FAIL  %s %s: %s\n", r.Candidate.Package, r.Candidate.Version, r.Error)
		}
	}

	fmt.Fprintf(out, "\n%d deleted, %d failed\n", result.Deleted, result.Failed)
	return result, nil
}

// RunSupersededDuplicatesGC plans and executes POINT-004's cleanup —
// SUPERSEDED generations that share their exact dependency+version with
// a currently ACTIVE generation, left behind by a check-then-create
// build race — a third, separate eligibility path from both RunGC's
// reference-based one and RunOrphanGC's never-promoted one, never
// combined into the same report or deletion run. It runs inside the
// daemon, against the stores it holds open, exactly like RunGC/RunOrphanGC.
func RunSupersededDuplicatesGC(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, dryRun bool, out io.Writer, vecReadiness *vectorReadiness) (api.GCResult, error) {
	candidates, err := retention.PlanSupersededDuplicateGC(ctx, store)
	if err != nil {
		return api.GCResult{}, fmt.Errorf("plan superseded-duplicate GC: %w", err)
	}

	result := api.GCResult{Candidates: len(candidates), DryRun: dryRun}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing eligible for superseded-duplicate garbage collection")
		return result, nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-8s %-45s %-15s %s\n", c.Ecosystem, c.Package, c.Version, c.GenerationID)
	}

	if dryRun {
		return result, nil
	}

	if usesEmbedder(cfg) {
		if err := vecReadiness.checkReady(); err != nil {
			return result, err
		}
	}
	vb, err := buildAllIndexes(cfg)
	if err != nil {
		return result, err
	}
	ns := backend.Namespace{Name: cfg.Vector.Collection}

	results, err := gc.RunSupersededDuplicates(ctx, store, badgerStore, vb, ns, candidates)
	if err != nil {
		return result, fmt.Errorf("run superseded-duplicate GC: %w", err)
	}

	for _, r := range results {
		if r.Succeeded {
			result.Deleted++
			fmt.Fprintf(out, "OK    %s %s %s\n", r.Candidate.Package, r.Candidate.Version, r.Candidate.GenerationID)
		} else {
			result.Failed++
			fmt.Fprintf(out, "FAIL  %s %s %s: %s\n", r.Candidate.Package, r.Candidate.Version, r.Candidate.GenerationID, r.Error)
		}
	}

	fmt.Fprintf(out, "\n%d deleted, %d failed\n", result.Deleted, result.Failed)
	return result, nil
}

// RunOrphanGC plans and executes GC-001/GC-002/GC-003's orphan-
// generation cleanup — a separate eligibility path from RunGC's
// reference-based one, never combined into the same report or deletion
// run (see docs/tickets/planned/22-orphan-lifecycle-gc). It runs inside
// the daemon, against the stores it holds open, exactly like RunGC.
func RunOrphanGC(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, dryRun bool, out io.Writer, vecReadiness *vectorReadiness) (api.GCResult, error) {
	candidates, err := retention.PlanOrphanGC(ctx, store, cfg.Vector.Backend, cfg.Retention.OrphanAge, time.Now())
	if err != nil {
		return api.GCResult{}, fmt.Errorf("plan orphan GC: %w", err)
	}

	result := api.GCResult{Candidates: len(candidates), DryRun: dryRun}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing eligible for orphan garbage collection")
		return result, nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-8s %-45s %-15s %-8s %-16s %s\n", c.Ecosystem, c.Package, c.Version, c.State, c.Reason, c.GenerationID)
	}

	if dryRun {
		return result, nil
	}

	if usesEmbedder(cfg) {
		if err := vecReadiness.checkReady(); err != nil {
			return result, err
		}
	}
	vb, err := buildAllIndexes(cfg)
	if err != nil {
		return result, err
	}
	ns := backend.Namespace{Name: cfg.Vector.Collection}

	results, err := gc.RunOrphans(ctx, store, badgerStore, vb, ns, candidates)
	if err != nil {
		return result, fmt.Errorf("run orphan GC: %w", err)
	}

	for _, r := range results {
		if r.Succeeded {
			result.Deleted++
			fmt.Fprintf(out, "OK    %s %s %s\n", r.Candidate.Package, r.Candidate.Version, r.Candidate.GenerationID)
		} else {
			result.Failed++
			fmt.Fprintf(out, "FAIL  %s %s %s: %s\n", r.Candidate.Package, r.Candidate.Version, r.Candidate.GenerationID, r.Error)
		}
	}

	fmt.Fprintf(out, "\n%d deleted, %d failed\n", result.Deleted, result.Failed)
	return result, nil
}

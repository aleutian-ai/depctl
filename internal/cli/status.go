package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/config"
	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/daemon/api"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/observability/metrics"
)

// backendHealthTimeout bounds the live backend probe, so an unreachable
// host can't stall `status` or `doctor`.
const backendHealthTimeout = 3 * time.Second

// storageMetricsRefreshInterval is how often refreshStorageMetrics
// recomputes OBS-003's ActiveGenerations/BadgerBytes gauges.
const storageMetricsRefreshInterval = 30 * time.Second

// Status, JobStats, and BackendStatus are the daemon API's status
// types; `depctl status` renders them as text or JSON.
type (
	Status        = api.Status
	JobStats      = api.JobStats
	BackendStatus = api.BackendStatus
)

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show system status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print status as JSON")
	return cmd
}

func runStatus(cmd *cobra.Command, jsonOut bool) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	st, err := c.Status(cmd.Context())
	if err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	printStatusText(cmd.OutOrStdout(), st)
	return nil
}

// buildStatus aggregates everything except backend health, which needs a
// live probe and is filled in by the caller.
func buildStatus(ctx context.Context, store *bboltstore.Store, backendName, controlPath, badgerPath string) (Status, error) {
	var st Status

	projects, err := store.ListProjects(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("list projects: %w", err)
	}
	st.Projects = len(projects)

	refs, err := store.ListAllReferences(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("list references: %w", err)
	}
	st.DependencyReferences = len(refs)

	pointers, err := store.ListActivePointers(ctx, backendName)
	if err != nil {
		return Status{}, fmt.Errorf("list active generations: %w", err)
	}
	st.ActiveGenerations = len(pointers)
	metrics.ActiveGenerations.Set(float64(st.ActiveGenerations))

	st.LastSync, err = lastPromotion(ctx, store, pointers)
	if err != nil {
		return Status{}, err
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("list jobs: %w", err)
	}
	st.Jobs = countJobs(jobs)

	if st.StorageBboltBytes, err = diskUsage(controlPath); err != nil {
		return Status{}, err
	}
	if st.StorageBadgerBytes, err = diskUsage(badgerPath); err != nil {
		return Status{}, err
	}
	metrics.BadgerBytes.Set(float64(st.StorageBadgerBytes))
	return st, nil
}

// refreshStorageMetrics periodically sets OBS-003's ActiveGenerations and
// BadgerBytes gauges (called as its own goroutine from runDaemonRun, only
// when metrics are enabled) — without this, those two gauges would only
// ever reflect whatever `depctl status` last happened to compute, so a
// real Prometheus scrape between status calls would read stale or
// zero-value data. Every other metric in this package is updated
// event-driven, at the same call site that already computes the value
// for OBS-001/002; these two have no such natural per-request call site
// (nothing on depctl's own request path needs "how many active
// generations exist right now"), so a small periodic refresh is the
// simplest fix — matching the existing checkEmbeddingReadiness/
// checkVectorReadiness background-goroutine pattern already established
// in this package.
func refreshStorageMetrics(ctx context.Context, store *bboltstore.Store, backendName, badgerPath string, interval time.Duration) {
	tick := func() {
		if pointers, err := store.ListActivePointers(ctx, backendName); err == nil {
			metrics.ActiveGenerations.Set(float64(len(pointers)))
		}
		if bytes, err := diskUsage(badgerPath); err == nil {
			metrics.BadgerBytes.Set(float64(bytes))
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// lastPromotion returns the newest UpdatedAt among active generations, or
// nil if nothing is active. It stands in for "last sync": depctl persists
// no per-run sync timestamp, and a no-op sync changes nothing to derive
// one from, so the last promotion is the last time a sync changed what
// queries see. Dangling pointers are skipped here; `depctl doctor`
// reports them.
func lastPromotion(ctx context.Context, store *bboltstore.Store, pointers []bboltstore.ActivePointer) (*time.Time, error) {
	var newest *time.Time
	for _, p := range pointers {
		gen, err := store.GetGeneration(ctx, p.GenerationID)
		if errors.Is(err, bboltstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get generation %s: %w", p.GenerationID, err)
		}
		if newest == nil || gen.UpdatedAt.After(*newest) {
			t := gen.UpdatedAt
			newest = &t
		}
	}
	return newest, nil
}

func countJobs(jobs []domain.Job) JobStats {
	var stats JobStats
	for _, j := range jobs {
		switch j.State {
		case domain.JobPending, domain.JobRetry:
			stats.Pending++
		case domain.JobRunning:
			stats.Running++
		case domain.JobFailed:
			stats.Failed++
		}
	}
	return stats
}

// diskUsage sums allocated bytes for a file or every file under a
// directory; a path that doesn't exist yet counts as zero.
func diskUsage(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += allocatedBytes(info)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("measure %s: %w", path, err)
	}
	return total, nil
}

// probeBackend runs the configured backend's health check; nil means
// healthy. An unsupported backend name is reported the same way as an
// unreachable one — neither can serve queries.
//
// It probes the vector store, or in keyword mode (which has none) the
// keyword index. Vector readiness relies on this: a managed Qdrant is
// started only when the vector store itself is unreachable.
func probeBackend(ctx context.Context, cfg config.Config) error {
	var vb backend.VectorBackend
	var err error
	if usesEmbedder(cfg) {
		vb, err = buildVectorBackend(cfg)
	} else {
		vb, err = buildSearchIndex(cfg, false)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, backendHealthTimeout)
	defer cancel()
	return vb.Health(ctx)
}

func printStatusText(out io.Writer, st Status) {
	health := "healthy"
	if !st.Backend.Healthy {
		health = "unhealthy (run `depctl doctor` for details)"
	}
	lastSync := "never"
	if st.LastSync != nil {
		lastSync = st.LastSync.Local().Format(time.RFC3339)
	}

	fmt.Fprintf(out, "%-22s %d\n", "projects:", st.Projects)
	fmt.Fprintf(out, "%-22s %d\n", "dependency references:", st.DependencyReferences)
	fmt.Fprintf(out, "%-22s %d\n", "active generations:", st.ActiveGenerations)
	fmt.Fprintf(out, "%-22s %d pending, %d running, %d failed\n", "jobs:", st.Jobs.Pending, st.Jobs.Running, st.Jobs.Failed)
	fmt.Fprintf(out, "%-22s %s\n", "storage (bbolt):", formatBytes(st.StorageBboltBytes))
	fmt.Fprintf(out, "%-22s %s\n", "storage (badger):", formatBytes(st.StorageBadgerBytes))
	fmt.Fprintf(out, "%-22s %s %s\n", "backend:", st.Backend.Name, health)
	fmt.Fprintf(out, "%-22s %s\n", "last sync:", lastSync)
	fmt.Fprintf(out, "%-22s %t\n", "gc running:", st.GCRunning)
	printSyncActivity(out, st.Syncs)
}

// printSyncActivity shows each in-flight sync's overall count and, under
// it, every dependency being built with its chunk progress — a coarse
// count alone sits still for minutes while one large dependency embeds.
func printSyncActivity(out io.Writer, syncs []api.ProjectSync) {
	if len(syncs) == 0 {
		return
	}
	fmt.Fprintf(out, "%-22s %d running\n", "syncs:", len(syncs))
	for _, s := range syncs {
		label := s.Root
		if label == "" {
			label = s.ProjectID
		}
		fmt.Fprintf(out, "  %s: %d of %d done", label, s.Done, s.Total)
		if s.Failed > 0 {
			fmt.Fprintf(out, ", %d failed", s.Failed)
		}
		fmt.Fprintln(out)
		// BATCH-001 Option D: observed timing + a labeled-confidence
		// estimate, once enough of the run has actually completed to say
		// anything — never a bare number with no sample-size context.
		if s.Estimate != nil {
			fmt.Fprintf(out, "    observed: median %.0fs/dep, p90 %.0fs (%d samples) — est. %.0fs remaining, confidence %s\n",
				s.Observed.MedianDependencySeconds, s.Observed.P90DependencySeconds, s.Observed.Samples, s.Estimate.RemainingSeconds, s.Estimate.Confidence)
		}
		// s.Pending (the full remaining-dependency list) deliberately
		// isn't printed here — this is a human terminal status view,
		// where hundreds of names would be noise; it's exposed through
		// sync_progress (MCP) and api.SyncProgress directly for a caller
		// that actually wants to act on the list.
		for _, d := range s.InFlight {
			if d.ChunksTotal > 0 {
				fmt.Fprintf(out, "    building %s: %d of %d chunks\n", d.Name, d.ChunksDone, d.ChunksTotal)
			} else {
				fmt.Fprintf(out, "    building %s\n", d.Name)
			}
		}
	}
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

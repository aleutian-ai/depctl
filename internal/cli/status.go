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

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/api"
	"aleutian-ai/ragctl/internal/domain"
)

// backendHealthTimeout bounds the live backend probe, so an unreachable
// host can't stall `status` or `doctor`.
const backendHealthTimeout = 3 * time.Second

// Status, JobStats, and BackendStatus are the daemon API's status
// types; `ragctl status` renders them as text or JSON.
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
	ctx := context.Background()

	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}

	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	controlPath, err := controlDBPath()
	if err != nil {
		return err
	}
	badgerPath, err := badgerDirPath()
	if err != nil {
		return err
	}

	st, err := buildStatus(ctx, store, cfg.Vector.Backend, controlPath, badgerPath)
	if err != nil {
		return err
	}
	st.Backend = BackendStatus{Name: cfg.Vector.Backend, Healthy: probeBackend(ctx, cfg) == nil}

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
	return st, nil
}

// lastPromotion returns the newest UpdatedAt among active generations, or
// nil if nothing is active. It stands in for "last sync": ragctl persists
// no per-run sync timestamp, and a no-op sync changes nothing to derive
// one from, so the last promotion is the last time a sync changed what
// queries see. Dangling pointers are skipped here; `ragctl doctor`
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
func probeBackend(ctx context.Context, cfg config.Config) error {
	vb, err := buildVectorBackend(cfg)
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
		health = "unhealthy (run `ragctl doctor` for details)"
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

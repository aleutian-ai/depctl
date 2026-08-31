package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/lifecycle/promote"
	"aleutian-ai/ragctl/internal/lifecycle/validate"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/retention"
	"aleutian-ai/ragctl/internal/source/git"
)

func newSyncCmd() *cobra.Command {
	var projectID, dependency string
	var dryRun, offline, force bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Execute the sync plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, projectID, dependency, dryRun, offline, force)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "limit to one project ID")
	cmd.Flags().StringVar(&dependency, "dependency", "", "limit to one dependency name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and exit without executing")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip actions that require network access")
	cmd.Flags().BoolVar(&force, "force", false, "promote a candidate even if VAL-002 sanity thresholds fail (structural/version-correctness failures are never forceable)")
	return cmd
}

func runSync(cmd *cobra.Command, projectID, dependency string, dryRun, offline, force bool) error {
	ctx := context.Background()

	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}

	if dryRun {
		plans, err := computePlans(ctx, store, cfg.Vector.Backend, projectID)
		if err != nil {
			return err
		}
		printPlans(cmd, plans)
		return nil
	}

	badgerStore, err := openDataStore()
	if err != nil {
		return fmt.Errorf("open data store: %w", err)
	}
	defer badgerStore.Close()

	_, failed, _, err := RunSync(ctx, store, badgerStore, cfg, projectID, dependency, offline, force, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d sync action(s) failed", failed)
	}
	return nil
}

// RunSync computes the plan for projectID (or every registered project,
// if projectID is empty — same as `ragctl sync` with no `--project`
// flag) and executes it: exported so internal/mcp's disabled-by-default
// `sync_project` tool (MCP-003) can trigger the identical sync logic
// `ragctl sync` uses, against the same already-open store/badger handles
// a long-running `ragctl serve` process holds — RunSync never opens its
// own Badger store, since Badger only allows one open handle per
// directory per process and the MCP server already holds one open for
// query.Service's whole lifetime.
func RunSync(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, projectID, dependency string, offline, force bool, out io.Writer) (synced, failed, skipped int, err error) {
	plans, err := computePlans(ctx, store, cfg.Vector.Backend, projectID)
	if err != nil {
		return 0, 0, 0, err
	}

	// The embedder/vector-backend/git pipeline touches the network, so
	// it's built lazily — only on the first SYNC_VERSION action that
	// actually needs it — never unconditionally. A no-op sync (nothing
	// to build) must make zero network calls, not just zero writes:
	// probing the embedder's Dimensions alone is a live HTTP call.
	var pipeline *syncPipeline
	getPipeline := func() (*syncPipeline, error) {
		if pipeline != nil {
			return pipeline, nil
		}
		embedder, err := buildEmbedder(cfg, badgerStore)
		if err != nil {
			return nil, err
		}
		vb, err := buildVectorBackend(cfg)
		if err != nil {
			return nil, err
		}
		gitCache, err := buildGitCache()
		if err != nil {
			return nil, err
		}
		dims, err := embedder.Dimensions(ctx)
		if err != nil {
			return nil, fmt.Errorf("probe embedder dimensions: %w", err)
		}
		pipeline = &syncPipeline{
			embedder: embedder,
			vb:       vb,
			gitCache: gitCache,
			ns:       backend.Namespace{Name: cfg.Vector.Collection, Dimensions: dims, Distance: "cosine"},
		}
		return pipeline, nil
	}

	reg, err := loadRegistryForCLI(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("load registry: %w", err)
	}

	for _, pp := range plans {
		if pp.Warning != "" {
			fmt.Fprintf(out, "%s: %s\n", pp.Project.Root, pp.Warning)
			continue
		}
		for _, action := range pp.Actions {
			if dependency != "" && action.Dependency.Dependency.Name != dependency {
				continue
			}

			switch action.Kind {
			case planner.ActionSyncVersion:
				if offline {
					fmt.Fprintf(out, "SKIP (offline)  %s %s\n", action.Dependency.Dependency.Name, action.Dependency.Version)
					skipped++
					continue
				}
				p, perr := getPipeline()
				if perr != nil {
					fmt.Fprintf(out, "FAIL  %s %s: %v\n", action.Dependency.Dependency.Name, action.Dependency.Version, perr)
					failed++
					continue
				}
				if err := syncVersion(ctx, store, badgerStore, p.gitCache, p.embedder, p.vb, p.ns, reg, action, force); err != nil {
					fmt.Fprintf(out, "FAIL  %s %s: %v\n", action.Dependency.Dependency.Name, action.Dependency.Version, err)
					failed++
					continue
				}
				fmt.Fprintf(out, "OK    %s %s\n", action.Dependency.Dependency.Name, action.Dependency.Version)
				synced++

			case planner.ActionAddReference:
				if err := addReference(ctx, store, action); err != nil {
					fmt.Fprintf(out, "FAIL  reference %s %s: %v\n", action.Dependency.Dependency.Name, action.Dependency.Version, err)
					failed++
				}

			case planner.ActionDropReference:
				dep := action.Dependency.Dependency
				if err := retention.DropReference(ctx, store, dep.Ecosystem, dep.Name, action.Dependency.Version, action.ProjectID); err != nil {
					fmt.Fprintf(out, "FAIL  drop reference %s: %v\n", action.Dependency.Dependency.Name, err)
					failed++
				}
			}
		}
	}

	fmt.Fprintf(out, "\n%d synced, %d failed, %d skipped\n", synced, failed, skipped)
	return synced, failed, skipped, nil
}

// addReference upserts the "project"-reason VersionReference a
// SYNC_VERSION/ADD_REFERENCE action implies. AddReference itself is
// idempotent (preserves FirstSeenAt across repeated calls), so this is a
// thin wrapper supplying the fields RET-001 needs.
func addReference(ctx context.Context, store *bboltstore.Store, action planner.Action) error {
	dep := action.Dependency.Dependency
	now := time.Now()
	return store.AddReference(ctx, domain.VersionReference{
		ProjectID:   action.ProjectID,
		Ecosystem:   dep.Ecosystem,
		Package:     dep.Name,
		Version:     action.Dependency.Version,
		Reason:      domain.ReferenceReasonProject,
		FirstSeenAt: now,
		LastSeenAt:  now,
	})
}

// syncPipeline bundles the network-touching providers syncVersion needs,
// built once (lazily) and reused across every SYNC_VERSION action in one
// `ragctl sync` run.
type syncPipeline struct {
	embedder embedding.Embedder
	vb       backend.VectorBackend
	gitCache *git.Cache
	ns       backend.Namespace
}

// syncVersion drives the full build->replicate->validate->promote
// pipeline for one SYNC_VERSION action.
func syncVersion(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, gitCache *git.Cache, embedder embedding.Embedder, vb backend.VectorBackend, ns backend.Namespace, reg *registry.Registry, action planner.Action, force bool) error {
	dep := action.Dependency
	manifest, ok := reg.Match(dep.Dependency.Ecosystem, dep.Dependency.Name)
	if !ok {
		return fmt.Errorf("no registry manifest for %s", dep.Dependency.Name)
	}

	gen, err := generation.Create(ctx, store, badgerStore, dep)
	if err != nil {
		return fmt.Errorf("create generation: %w", err)
	}
	if err := generation.Build(ctx, gen, manifest.Sources, gitCache, store, badgerStore); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := generation.Replicate(ctx, gen, manifest.Sources, embedder, vb, ns, store, badgerStore); err != nil {
		return fmt.Errorf("replicate: %w", err)
	}

	genManifest, err := readGenerationManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	replica, err := store.GetBackendReplica(ctx, gen.ID, vb.Name())
	if err != nil {
		return fmt.Errorf("read replica: %w", err)
	}

	var prior *domain.Generation
	var priorManifest *generation.Manifest
	if p, err := store.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, vb.Name()); err == nil {
		prior = &p
		if pm, err := readGenerationManifest(ctx, badgerStore, p.ID); err == nil {
			priorManifest = &pm
		}
	}

	gen, report, err := validate.Run(ctx, gen, genManifest, replica, prior, priorManifest, validate.DefaultSanityConfig(), embedder, vb, ns, store, badgerStore)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	sanity := report.Sanity
	if !sanity.Passed && force {
		// Only VAL-002 (Sanity) is forceable — structural and
		// version-correctness failures indicate the replica itself is
		// broken, never something a user should override.
		sanity = validate.StructuralResult{Passed: true, Failures: []string{"sanity check forced: " + fmt.Sprint(report.Sanity.Failures)}}
	}
	if !report.Structural.Passed || !sanity.Passed || !report.VersionCorrectness.Passed {
		return fmt.Errorf("validation failed: %v", append(append(report.Structural.Failures, sanity.Failures...), report.VersionCorrectness.Failures...))
	}

	return promote.Promote(ctx, store, gen, vb.Name(), report.Structural, sanity, report.VersionCorrectness)
}

func readGenerationManifest(ctx context.Context, badgerStore *badgerstore.Store, generationID string) (generation.Manifest, error) {
	data, err := badgerStore.GetManifest(ctx, generationID)
	if err != nil {
		return generation.Manifest{}, err
	}
	var m generation.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return generation.Manifest{}, err
	}
	return m, nil
}

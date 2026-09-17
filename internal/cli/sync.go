package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon"
	"aleutian-ai/ragctl/internal/daemon/api"
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
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}

	if dryRun {
		var plans []projectPlan
		if err := c.Plan(cmd.Context(), projectID, &plans); err != nil {
			return err
		}
		printPlans(cmd, plans)
		return nil
	}

	req := api.SyncRequest{ProjectID: projectID, Dependency: dependency, Offline: offline, Force: force}
	resp, err := c.Sync(cmd.Context(), req, cmd.OutOrStdout())
	if err != nil {
		return err
	}

	var failed int
	for _, r := range resp.Results {
		failed += r.Failed
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
func RunSync(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, projectID, dependency string, offline, force bool, out io.Writer, readiness *embeddingReadiness, vecReadiness *vectorReadiness, priority *daemon.SyncPriority) (synced, failed, skipped int, err error) {
	plans, err := computePlans(ctx, store, cfg.Vector.Backend, projectID)
	if err != nil {
		return 0, 0, 0, err
	}

	// The embedder/vector-backend/git pipeline touches the network, so
	// it's built lazily — only on the first SYNC_VERSION action that
	// actually needs it — never unconditionally. A no-op sync (nothing
	// to build) must make zero network calls, not just zero writes:
	// probing the embedder's Dimensions alone is a live HTTP call.
	//
	// The readiness check happens every call, not just when pipeline is
	// nil, for the same reason fullQueryService's does: a "still
	// pulling" result must never get treated as if it were the real,
	// memoized pipeline build.
	var pipeline *syncPipeline
	getPipeline := func() (*syncPipeline, error) {
		if pipeline != nil {
			return pipeline, nil
		}
		if err := readiness.checkReady(); err != nil {
			return nil, err
		}
		if err := vecReadiness.checkReady(); err != nil {
			return nil, err
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

	// Flattened into one ordered queue up front, matching exactly the
	// traversal order a simple nested range over plans/pp.Actions would
	// produce (project-by-project, action-by-action) — with no priority
	// bumps ever arriving, popping from the front reproduces that order
	// byte-for-byte (WATCH-020's regression guard). A bump moves its
	// named dependency's SYNC_VERSION action to the very front of
	// whatever's left, ahead of any other action kind too — "prioritize"
	// means it runs next, not just next among other syncs.
	var queue []planner.Action
	for _, pp := range plans {
		if pp.Warning != "" {
			fmt.Fprintf(out, "%s: %s\n", pp.Project.Root, pp.Warning)
			continue
		}
		for _, action := range pp.Actions {
			if dependency != "" && action.Dependency.Dependency.Name != dependency {
				continue
			}
			queue = append(queue, action)
		}
	}

	for len(queue) > 0 {
		for _, dep := range priority.Drain() {
			queue = bumpActionToFront(queue, dep)
		}
		action := queue[0]
		queue = queue[1:]

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

	fmt.Fprintf(out, "\n%d synced, %d failed, %d skipped\n", synced, failed, skipped)
	return synced, failed, skipped, nil
}

// bumpActionToFront moves the first SYNC_VERSION action for dependency
// to the front of queue, if one is still there — a no-op if it's
// already been processed, or was never part of this run at all
// (WATCH-020's bump-after-the-fact case; the caller times out cleanly
// rather than hanging, per that ticket's failure behavior).
func bumpActionToFront(queue []planner.Action, dependency string) []planner.Action {
	for i, a := range queue {
		if a.Kind != planner.ActionSyncVersion || a.Dependency.Dependency.Name != dependency {
			continue
		}
		if i == 0 {
			return queue
		}
		reordered := make([]planner.Action, 0, len(queue))
		reordered = append(reordered, a)
		reordered = append(reordered, queue[:i]...)
		reordered = append(reordered, queue[i+1:]...)
		return reordered
	}
	return queue
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

// fallbackManifest derives a minimal, single-source manifest (REG-005)
// for a dependency with no registry match, when its own identity already
// carries a fetchable repo location — a Go module path directly shaped
// like github.com/<org>/<repo>[/...], or (REG-008) a vanity import path
// resolved via an HTTP go-import lookup, the same one `go get` itself
// performs. The derived source is a real "git" type pointed at the
// package's own repository, so it gets TrustRepository like any other
// git source (TrustClassForSourceType, SEC-001) — the fact that nobody
// hand-authored the YAML doesn't make the content itself less
// authoritative, only its authority ranking, which is deliberately 0.
func fallbackManifest(ctx context.Context, dep domain.Dependency) (registry.Manifest, bool) {
	if dep.Ecosystem != domain.EcosystemGo {
		return registry.Manifest{}, false
	}
	url, ok := githubModuleURL(dep.Name)
	if !ok {
		url, ok = resolveVanityImport(ctx, dep.Name)
	}
	if !ok {
		return registry.Manifest{}, false
	}
	return registry.Manifest{
		Metadata: registry.Metadata{Name: dep.Name},
		Match:    registry.Match{Ecosystems: []domain.Ecosystem{dep.Ecosystem}, Packages: []string{dep.Name}},
		Version:  registry.VersionStrategy{Strategy: "none"},
		Sources:  []registry.Source{{ID: "repository", Type: "git", URL: url, Ref: "HEAD", Authority: 0}},
	}, true
}

// githubModuleURL is the fast path: a Go module path already directly
// shaped like github.com/<org>/<repo>[/...] needs no lookup at all.
func githubModuleURL(modulePath string) (string, bool) {
	segments := strings.Split(modulePath, "/")
	if len(segments) < 3 || segments[0] != "github.com" {
		return "", false
	}
	return "https://github.com/" + segments[1] + "/" + segments[2], true
}

// vanityImportTimeout bounds the go-import meta-tag HTTP lookup so a
// slow or unresponsive vanity-import host can't stall a sync.
const vanityImportTimeout = 5 * time.Second

// goImportMetaTag matches Go's documented go-import meta tag:
// https://go.dev/ref/mod#vcs-branch
var goImportMetaTag = regexp.MustCompile(`<meta\s+name=["']go-import["']\s+content=["']([^"']+)["']\s*/?>`)

// vanityImportHTTPClient issues resolveVanityImport's lookup — a var so
// tests can redirect it to a local httptest.Server instead of making a
// real network call against an actual vanity-import host.
var vanityImportHTTPClient = http.DefaultClient

// resolveVanityImport performs the same lookup `go get` uses for a
// module path with no known VCS host: GET .../<path>?go-get=1 and parse
// the go-import meta tag out of the response. Only a git-VCS result is
// usable — nothing else in ragctl can acquire from a source.
func resolveVanityImport(ctx context.Context, modulePath string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, vanityImportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+modulePath+"?go-get=1", nil)
	if err != nil {
		return "", false
	}
	resp, err := vanityImportHTTPClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}

	for _, m := range goImportMetaTag.FindAllStringSubmatch(string(body), -1) {
		fields := strings.Fields(m[1])
		if len(fields) != 3 {
			continue
		}
		root, vcs, repoURL := fields[0], fields[1], fields[2]
		if vcs != "git" {
			continue
		}
		if modulePath == root || strings.HasPrefix(modulePath, root+"/") {
			return repoURL, true
		}
	}
	return "", false
}

// syncVersion drives the full build->replicate->validate->promote
// pipeline for one SYNC_VERSION action.
func syncVersion(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, gitCache *git.Cache, embedder embedding.Embedder, vb backend.VectorBackend, ns backend.Namespace, reg *registry.Registry, action planner.Action, force bool) error {
	dep := action.Dependency
	manifest, ok := reg.Match(dep.Dependency.Ecosystem, dep.Dependency.Name)
	if !ok {
		manifest, ok = fallbackManifest(ctx, dep.Dependency)
		if !ok {
			return fmt.Errorf("no registry manifest for %s", dep.Dependency.Name)
		}
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

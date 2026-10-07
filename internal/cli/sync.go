package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	"aleutian-ai/ragctl/internal/httplimit"
	"aleutian-ai/ragctl/internal/lifecycle/promote"
	"aleutian-ai/ragctl/internal/lifecycle/validate"
	"aleutian-ai/ragctl/internal/observability/metrics"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/retention"
	"aleutian-ai/ragctl/internal/source/git"
)

func newSyncCmd() *cobra.Command {
	var projectID string
	var dependencies []string
	var dryRun, offline, force, rebuild bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Execute the sync plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, projectID, dependencies, dryRun, offline, force, rebuild)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "limit to one project ID")
	cmd.Flags().StringArrayVar(&dependencies, "dependency", nil, "limit to a dependency name (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and exit without executing")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip actions that require network access")
	cmd.Flags().BoolVar(&force, "force", false, "promote a candidate even if VAL-002 sanity thresholds fail (structural/version-correctness failures are never forceable)")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "force a genuine rebuild of each --dependency even though its version hasn't changed (OPS-004) — for a generation ragctl doctor flagged as active but empty; requires --dependency, not supported with --dry-run")
	return cmd
}

func runSync(cmd *cobra.Command, projectID string, dependencies []string, dryRun, offline, force, rebuild bool) error {
	if rebuild && len(dependencies) == 0 {
		return fmt.Errorf("--rebuild requires at least one --dependency — it forces a rebuild of exactly the named dependencies, never every dependency in scope")
	}
	if rebuild && dryRun {
		return fmt.Errorf("--rebuild is not supported with --dry-run — the plan it would print doesn't reflect the rebuild's own effect, since --dry-run only reads the plan the daemon already has")
	}

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

	req := api.SyncRequest{ProjectID: projectID, Dependencies: dependencies, Offline: offline, Force: force, Rebuild: rebuild}
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

// syncActionConcurrencyHook, if set, runs once per action while its
// daemonSem slot is held — test-only instrumentation for proving the
// daemon-wide cap actually bounds concurrency across separate RunSync
// calls (simulating separate projects, each with its own worker pool),
// since neither an offline SYNC_VERSION skip nor reference bookkeeping
// normally takes long enough to observably overlap on their own.
var syncActionConcurrencyHook = func() {}

// clearForRebuild implements OPS-004 (epic 61): `ragctl sync --rebuild
// --dependency X` clears X's active-generation pointer and this scope's
// version reference(s) *before* planning, so planner.Plan's own
// version-unchanged NOOP branch (internal/planner/planner.go) never gets
// a chance to fire. This is the only way to force a genuine rebuild of a
// generation whose real backend content is gone despite otherwise-
// unchanged, healthy-looking bookkeeping — `ragctl doctor`'s "empty
// active generations" check exists to detect exactly that state, and
// plain --force cannot fix it (it only affects an already-selected
// action, never which actions the planner selects). Live-found and
// live-fixed this way for a real, ~132-generation incident (see
// docs/architecture.md's OPS-003/OPS-004 section) before this command
// existed — that fix needed a throwaway Go program against the real
// database; this is the real, supported equivalent.
//
// projectID empty means every registered project, matching RunSync's
// own convention — every project whose current resolution includes one
// of dependencyNames gets that dependency's bookkeeping cleared, scoped
// to exactly the named dependencies and nothing else in the same
// project.
func clearForRebuild(ctx context.Context, store *bboltstore.Store, backendName, projectID string, dependencyNames []string) error {
	wanted := make(map[string]bool, len(dependencyNames))
	for _, name := range dependencyNames {
		wanted[name] = true
	}

	var projects []domain.Project
	if projectID != "" {
		p, err := store.GetProject(ctx, projectID)
		if err != nil {
			return fmt.Errorf("get project %s: %w", projectID, err)
		}
		projects = []domain.Project{p}
	} else {
		var err error
		projects, err = store.ListProjects(ctx)
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
	}

	for _, p := range projects {
		resolution, err := store.GetResolution(ctx, p.ID)
		if err != nil {
			continue // no resolution yet for this project — nothing to rebuild
		}
		for _, dep := range resolution.Dependencies {
			if !wanted[dep.Dependency.Name] {
				continue
			}
			if err := store.RemoveReference(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version, p.ID); err != nil {
				return fmt.Errorf("clear reference for %s: %w", dep.Dependency.Name, err)
			}
			if err := store.ClearActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version, backendName); err != nil {
				return fmt.Errorf("clear active generation for %s: %w", dep.Dependency.Name, err)
			}
		}
	}
	return nil
}

// RunSync computes the plan for projectID (every registered project if
// empty, like `ragctl sync` without --project) and executes it, inside the
// daemon against the stores it already holds open.
//
// coordinator gates every generation build and reference change against
// concurrent GC and duplicate builds of the same generation (see
// daemon.BuildCoordinator). daemonSem, if non-nil, is the daemon-wide
// semaphore sized by sync.max_total_concurrency, held only around each
// action's build and replicate step so many projects syncing at once stay
// bounded; nil means uncapped.
func RunSync(ctx context.Context, coordinator *daemon.BuildCoordinator, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, projectID string, dependencies []string, offline, force, rebuild bool, out io.Writer, readiness *embeddingReadiness, vecReadiness *vectorReadiness, priority *daemon.SyncPriority, progress *daemon.SyncProgress, daemonSem chan struct{}, gitCache *git.Cache) (synced, failed, skipped int, err error) {
	// SEC-003: applied once per call, ahead of any fallback-manifest
	// fetch this run might trigger. A plain package var, not threaded as
	// a parameter through the several fallback-manifest call layers
	// below (registry matching -> npm/pypi/vanity-import resolution) —
	// deliberately, matching this codebase's existing convention for
	// rarely-changing, process-wide tunables (e.g. vanityImportTimeout).
	// Atomic because epic 53 runs syncs concurrently; every concurrent
	// RunSync in a real daemon stores the same on-disk config's value.
	limits := cfg.Fetch
	syncFetchLimits.Store(&limits)
	if rebuild && len(dependencies) > 0 {
		if err := clearForRebuild(ctx, store, cfg.Vector.Backend, projectID, dependencies); err != nil {
			return 0, 0, 0, fmt.Errorf("rebuild: %w", err)
		}
	}
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
	// memoized pipeline build. pipelineMu guards the lazy build itself
	// (not the readiness checks, which can run concurrently) — COORD-003
	// made getPipeline reachable from multiple worker goroutines at
	// once, and an unguarded read/write of pipeline would race.
	//
	// retrieval.mode (LOCAL-002) decides what gets built: keyword never
	// touches the embedder or vector store, so it has no readiness gate
	// at all; vector requires both, as before; auto builds with vectors
	// when both are ready and keyword-only otherwise (backfilled later).
	var pipelineMu sync.Mutex
	var pipeline, keywordPipeline *syncPipeline
	mode := cfg.Retrieval.ModeOrDefault()
	getKeywordPipeline := func() (*syncPipeline, error) {
		pipelineMu.Lock()
		defer pipelineMu.Unlock()
		if keywordPipeline != nil {
			return keywordPipeline, nil
		}
		idx, err := buildSearchIndex(cfg, false)
		if err != nil {
			return nil, err
		}
		keywordPipeline = &syncPipeline{vb: idx, gitCache: gitCache, ns: backend.Namespace{Name: cfg.Vector.Collection}}
		return keywordPipeline, nil
	}
	getPipeline := func() (*syncPipeline, error) {
		switch mode {
		case config.RetrievalKeyword:
			return getKeywordPipeline()
		case config.RetrievalAuto:
			if !vectorsReady(readiness, vecReadiness) {
				return getKeywordPipeline()
			}
		default:
			if err := readiness.checkReady(); err != nil {
				return nil, err
			}
			if err := vecReadiness.checkReady(); err != nil {
				return nil, err
			}
		}
		pipelineMu.Lock()
		defer pipelineMu.Unlock()
		if pipeline != nil {
			return pipeline, nil
		}
		embedder, err := buildEmbedder(cfg, badgerStore)
		if err != nil {
			return nil, err
		}
		idx, err := buildSearchIndex(cfg, true)
		if err != nil {
			return nil, err
		}
		dims, err := embedder.Dimensions(ctx)
		if err != nil {
			return nil, fmt.Errorf("probe embedder dimensions: %w", err)
		}
		pipeline = &syncPipeline{
			embedder: embedder,
			vb:       idx,
			vector:   idx.vector,
			gitCache: gitCache,
			ns:       backend.Namespace{Name: cfg.Vector.Collection, Dimensions: dims, Distance: "cosine"},
		}
		return pipeline, nil
	}

	reg, err := loadRegistryForCLI(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("load registry: %w", err)
	}

	// Versions built before this install used keyword search get keyword
	// entries (local, no model), and versions synced while embeddings
	// were unavailable get their vectors now that they are. Vector
	// backfill is checked from the store first, so a sync with nothing to
	// backfill still makes no network calls. Both are best-effort: a
	// failure never fails the sync, and the next one retries.
	if mode != config.RetrievalVector {
		if err := backfillKeyword(ctx, store, badgerStore, cfg, reg, out); err != nil {
			fmt.Fprintf(out, "%-12s %v (will retry on the next sync)\n", "KEYWORD", err)
		}
	}
	if !offline && mode != config.RetrievalKeyword && hasKeywordOnlyGenerations(ctx, store, cfg) {
		if p, err := getPipeline(); err == nil && p.embedder != nil {
			if err := backfillVectors(ctx, store, badgerStore, cfg, reg, p.embedder, p.vector, p.ns, out); err != nil {
				fmt.Fprintf(out, "%-12s %v (will retry on the next sync)\n", "VECTORS", err)
			}
		}
	}

	// Flattened into one ordered queue up front, matching exactly the
	// traversal order a simple nested range over plans/pp.Actions would
	// produce (project-by-project, action-by-action) — with no priority
	// bumps ever arriving, popping from the front reproduces that order
	// byte-for-byte (WATCH-020's regression guard). A bump moves its
	// named dependency's SYNC_VERSION action to the very front of
	// whatever's left, ahead of any other action kind too — "prioritize"
	// means it runs next, not just next among other syncs.
	actions := flattenActions(plans, dependencies, out)
	// MCP-004: Total/Done/Failed are specifically about *syncing a
	// dependency's content* — only ActionSyncVersion represents that.
	// NOOP ("nothing changed, nothing to plan") and the reference-
	// bookkeeping kinds (ADD_REFERENCE/DROP_REFERENCE/GC_CANDIDATE) carry
	// no information about sync success at all, so they must never count
	// toward it — live-found: a re-sync of two dependencies whose real
	// SYNC_VERSION attempt had just failed replanned as two NOOP actions
	// (their reference hadn't changed), and the old "count every action"
	// accounting reported that as "2 of 2 done, 0 failed" — indistinguishable
	// from a real success. runSyncAction's own progress.Finish call below
	// is scoped to match (only for ActionSyncVersion, mirroring
	// progress.Begin's own existing scoping).
	syncVersionCount := 0
	var syncVersionNames []string
	for _, a := range actions {
		if a.Kind == planner.ActionSyncVersion {
			syncVersionCount++
			syncVersionNames = append(syncVersionNames, a.Dependency.Dependency.Name)
		}
	}
	progress.SetTotal(syncVersionCount)
	// BATCH-001 Option D: the full remaining-scope list, distinct from
	// the plain count above — see SyncProgress.SetPlanned's own doc
	// comment.
	progress.SetPlanned(syncVersionNames)

	// N workers pull from the same queue concurrently (epic 53/
	// COORD-003) — bounded by cfg.Sync.MaxConcurrency, defaulting to 2
	// for a config predating this field (0 unmarshals as "unset", never
	// rejected by Validate — see SyncConfig's own doc comment for why
	// this default was chosen conservatively rather than measured to be
	// optimal). A single-action JIT sync (len(actions) <= 1) never
	// benefits from more than one worker regardless of this setting.
	n := cfg.Sync.MaxConcurrency
	if n < 1 {
		n = 2
	}
	if n > len(actions) {
		n = max(len(actions), 1)
	}

	queue := newSyncQueue(actions)
	var mu, outMu sync.Mutex
	var completed int
	var budgetExceeded sync.Once
	writeLine := func(format string, args ...any) {
		outMu.Lock()
		defer outMu.Unlock()
		fmt.Fprintf(out, format, args...)
	}

	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// BATCH-001 Option C: the shared batch context
				// (internal/daemon/scheduler.go's maxActionDuration) bounds
				// this whole RunSync call, not any single action —
				// dependencySyncTimeout above only stops one huge
				// dependency from starving the rest of the batch's share
				// of that same clock. Once the aggregate clock itself is
				// gone, every worker would otherwise pop and immediately
				// fail its next action with the same generic "context
				// deadline exceeded," cascading into a wall of messages
				// that look like independent per-dependency failures
				// (STRESS-005/epic 51's live-found bug: 407 of 561 this
				// way in one real run). Checked here, before popping, so
				// a worker stops cleanly instead of doing that — and
				// budgetExceeded.Do ensures exactly one aggregate message
				// is written regardless of how many workers notice at
				// once.
				if ctx.Err() != nil {
					budgetExceeded.Do(func() {
						mu.Lock()
						done := completed
						mu.Unlock()
						writeLine("\nsync time budget exceeded after %d of %d actions completed; re-run to continue with the remaining %d\n", done, len(actions), len(actions)-done)
					})
					return
				}
				action, ok := queue.next(priority)
				if !ok {
					return
				}
				if daemonSem != nil {
					daemonSem <- struct{}{}
				}
				syncActionConcurrencyHook()
				s, f, sk := runSyncAction(ctx, coordinator, store, badgerStore, reg, getPipeline, action, offline, force, progress, writeLine)
				if daemonSem != nil {
					<-daemonSem
				}
				mu.Lock()
				synced += s
				failed += f
				skipped += sk
				completed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if ctx.Err() == nil {
		writeLine("\n%d synced, %d failed, %d skipped\n", synced, failed, skipped)
	}
	return synced, failed, skipped, nil
}

// syncQueue is a priority-bumpable action queue safe for concurrent
// workers to pop from — the same WATCH-020 bump-to-front reordering
// RunSync's previous single-consumer loop had, just synchronized now
// that more than one goroutine drains it (epic 53/COORD-003).
type syncQueue struct {
	mu      sync.Mutex
	actions []planner.Action
}

func newSyncQueue(actions []planner.Action) *syncQueue {
	return &syncQueue{actions: actions}
}

// next drains any pending priority bumps, then pops the front action.
// Reports false once the queue is empty — a worker's signal to exit.
func (q *syncQueue) next(priority *daemon.SyncPriority) (planner.Action, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, dep := range priority.Drain() {
		q.actions = bumpActionToFront(q.actions, dep)
	}
	if len(q.actions) == 0 {
		return planner.Action{}, false
	}
	action := q.actions[0]
	q.actions = q.actions[1:]
	return action, true
}

// flattenActions orders every project's planned actions into one queue,
// project by project, keeping only actions for the named dependencies
// (all of them when dependencies is empty). A project with a warning
// contributes the warning, not actions.
func flattenActions(plans []projectPlan, dependencies []string, out io.Writer) []planner.Action {
	wanted := make(map[string]bool, len(dependencies))
	for _, name := range dependencies {
		wanted[name] = true
	}
	var actions []planner.Action
	for _, pp := range plans {
		if pp.Warning != "" {
			fmt.Fprintf(out, "%s: %s\n", pp.Project.Root, pp.Warning)
			continue
		}
		for _, action := range pp.Actions {
			if len(wanted) > 0 && !wanted[action.Dependency.Dependency.Name] {
				continue
			}
			actions = append(actions, action)
		}
	}
	return actions
}

// dependencySyncTimeout bounds one SYNC_VERSION action's own real work —
// replacing the old whole-batch maxActionDuration ceiling as the thing
// that actually bounds a single dependency (STRESS-005/epic 51's own
// finding: a single huge dependency, alibaba-cloud-sdk-go-shaped,
// shouldn't be able to starve every other worker sharing one aggregate
// deadline). Each worker gets its own budget, independent of how long
// the rest of the batch takes or how many other workers are running. A
// var, not a const, so a test can shorten it (matches maxActionDuration's
// own convention in internal/daemon/scheduler.go).
var dependencySyncTimeout = 10 * time.Minute

// runSyncAction runs one action to completion (never returning an error
// itself — every outcome is written to out and reflected in its
// synced/failed/skipped return, so one worker's failure can never
// propagate into cancelling another's unrelated in-flight work, unlike
// errgroup's default cancel-on-first-error behavior).
func runSyncAction(ctx context.Context, coordinator *daemon.BuildCoordinator, store *bboltstore.Store, badgerStore *badgerstore.Store, reg *registry.Registry, getPipeline func() (*syncPipeline, error), action planner.Action, offline, force bool, progress *daemon.SyncProgress, writeLine func(string, ...any)) (synced, failed, skipped int) {
	// OBS-003: recorded once, on every exit path, regardless of which
	// switch case below actually ran — same reasoning as RunGC's own
	// defer-based metric/log recording (internal/cli/gc.go).
	defer func() {
		state := "success"
		switch {
		case failed > 0:
			state = "failure"
			metrics.SyncFailuresTotal.Inc()
		case skipped > 0:
			state = "skipped"
		}
		metrics.SyncJobsTotal.WithLabelValues(string(action.Kind), state).Inc()
	}()

	name := action.Dependency.Dependency.Name
	if action.Kind == planner.ActionSyncVersion {
		progress.Begin(name)
		ctx = generation.WithProgress(ctx, func(done, total int) { progress.SetChunks(name, done, total) })
		// MCP-004: scoped to match progress.Begin and SetTotal's own
		// scoping above — Finish must only fire for the action kind that
		// actually represents "syncing a dependency," or Done/Total drift
		// out of sync with each other (Total no longer counts NOOP/
		// bookkeeping actions, so Finish must not either).
		defer func() { progress.Finish(name, failed > 0) }()
	}

	switch action.Kind {
	case planner.ActionSyncVersion:
		if offline {
			writeLine("SKIP (offline)  %s %s\n", action.Dependency.Dependency.Name, action.Dependency.Version)
			return 0, 0, 1
		}
		p, perr := getPipeline()
		if perr != nil {
			writeLine("FAIL  %s %s: %v\n", action.Dependency.Dependency.Name, action.Dependency.Version, perr)
			return 0, 1, 0
		}
		actionCtx, cancel := context.WithTimeout(ctx, dependencySyncTimeout)
		defer cancel()
		// coordinator.Build both excludes concurrent GC and coalesces
		// any other concurrent request for this identical generation
		// into this one real build (epic 53/COORD-001..002).
		buildErr := coordinator.Build(action.Dependency, func() error {
			return syncVersion(actionCtx, store, badgerStore, p.gitCache, p.embedder, p.vb, p.ns, reg, action, force)
		})
		if buildErr != nil {
			writeLine("FAIL  %s %s: %v\n", action.Dependency.Dependency.Name, action.Dependency.Version, buildErr)
			return 0, 1, 0
		}
		writeLine("OK    %s %s\n", action.Dependency.Dependency.Name, action.Dependency.Version)
		return 1, 0, 0

	case planner.ActionAddReference:
		// PlanGC reads the references bucket directly, so this write
		// needs the same GC exclusion a real build gets, even though
		// there's no build identity here to coalesce.
		refErr := coordinator.ProtectFromGC(func() error {
			return addReference(ctx, store, action)
		})
		if refErr != nil {
			writeLine("FAIL  reference %s %s: %v\n", action.Dependency.Dependency.Name, action.Dependency.Version, refErr)
			return 0, 1, 0
		}
		return 0, 0, 0

	case planner.ActionDropReference:
		dep := action.Dependency.Dependency
		dropErr := coordinator.ProtectFromGC(func() error {
			return retention.DropReference(ctx, store, dep.Ecosystem, dep.Name, action.Dependency.Version, action.ProjectID)
		})
		if dropErr != nil {
			writeLine("FAIL  drop reference %s: %v\n", action.Dependency.Dependency.Name, dropErr)
			return 0, 1, 0
		}
		return 0, 0, 0
	}
	return 0, 0, 0
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

// syncPipeline is what one sync builds with, built lazily and reused
// across every SYNC_VERSION action in the run. embedder and vector are nil
// for a keyword-only build; vb is every index the build writes.
type syncPipeline struct {
	embedder *embedding.Prompted
	vb       backend.VectorBackend
	vector   backend.VectorBackend
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
	switch dep.Ecosystem {
	case domain.EcosystemGo:
		return goFallbackManifest(ctx, dep)
	case domain.EcosystemNode:
		return npmFallbackManifest(ctx, dep)
	case domain.EcosystemPython:
		return pypiFallbackManifest(ctx, dep)
	default:
		return registry.Manifest{}, false
	}
}

// goFallbackManifest is REG-005/REG-008's original Go fallback: a module
// path already carries its own repository location, and go.sum's version
// pinning means a version's own tag is a safe, deterministic ref.
func goFallbackManifest(ctx context.Context, dep domain.Dependency) (registry.Manifest, bool) {
	root, url, ok := githubModuleURL(dep.Name)
	if !ok {
		root, url, ok = resolveVanityImport(ctx, dep.Name)
	}
	if !ok {
		return registry.Manifest{}, false
	}
	subdir := moduleSubdir(dep.Name, root)
	return registry.Manifest{
		Metadata: registry.Metadata{Name: dep.Name},
		Match:    registry.Match{Ecosystems: []domain.Ecosystem{dep.Ecosystem}, Packages: []string{dep.Name}},
		Version:  registry.VersionStrategy{Strategy: "none"},
		Sources:  []registry.Source{{ID: "repository", Type: "git", URL: url, Ref: moduleTagTemplate(subdir), Subdir: subdir, Authority: 0}},
	}, true
}

// npmFallbackManifest is REG-012: unlike Go, an npm package name carries
// no repository location, and a published version has no guaranteed
// matching git tag — so the repository comes from a registry lookup, and
// every real-world tag convention is tried, each verified to actually
// exist before being trusted (never a bare branch-head fallback; see
// refCandidates/resolveFirstRef in internal/data/generation/build.go,
// which this manifest's RefTemplates feed).
func npmFallbackManifest(ctx context.Context, dep domain.Dependency) (registry.Manifest, bool) {
	url, subdir, ok := npmRepository(ctx, dep.Name)
	if !ok {
		return registry.Manifest{}, false
	}
	return registry.Manifest{
		Metadata: registry.Metadata{Name: dep.Name},
		Match:    registry.Match{Ecosystems: []domain.Ecosystem{dep.Ecosystem}, Packages: []string{dep.Name}},
		Version:  registry.VersionStrategy{Strategy: "none"},
		Sources: []registry.Source{{
			ID: "repository", Type: "git", URL: url, Subdir: subdir, Authority: 0,
			RefTemplates: npmTagCandidates(dep.Name, subdir),
		}},
	}, true
}

// npmTagCandidates lists the git tag conventions real npm packages
// actually use. subdir is the registry's reported monorepo directory (see
// npmRepository), if any — when set, ONLY package-scoped tag shapes
// ("<package>@<version>"/"<package>-v<version>") are tried. A bare
// "v<version>"/"<version>" tag in a monorepo hosting many packages is
// ambiguous across every package's own version history, not just this
// one's: verified live against eslint-visitor-keys 3.4.3 (repository
// github.com/eslint/js, directory packages/eslint-visitor-keys) — the
// bare tag "v3.4.3" resolves, but to ESLint core's own 2016 release, a
// completely unrelated package's version number that happens to collide
// in the same repo's tag namespace. Trusting it would have silently
// indexed the wrong package's content under a confident version label,
// exactly the failure class POINT-002's version-exactness fix eliminated
// for Go — a subdir check happened to catch this specific instance (the
// old commit predates the monorepo split), but a bare-tag match isn't
// safe to trust in general, so it's never offered as a candidate at all
// once a monorepo directory is known. A single-package repo (no subdir)
// has no such ambiguity, so bare tags are tried there.
func npmTagCandidates(name, subdir string) []string {
	if subdir != "" {
		return []string{name + "@${version}", name + "-v${version}"}
	}
	return []string{"v${version}", "${version}", name + "@${version}", name + "-v${version}"}
}

// syncFetchLimits holds this run's SEC-003 fetch limits, applied once at
// RunSync's entry — see its own assignment there for why this is a
// package var rather than a threaded parameter.
var syncFetchLimits atomic.Pointer[config.FetchConfig]

// currentFetchLimits is the latest sync's fetch limits, or the defaults
// (FetchConfig's zero value) before any sync has run.
func currentFetchLimits() config.FetchConfig {
	if p := syncFetchLimits.Load(); p != nil {
		return *p
	}
	return config.FetchConfig{}
}

// fetchLimitRedirectPolicy is shared by all three registry/vanity-import
// HTTP clients below — it reads syncFetchLimits at redirect time (not at
// client-construction time, which happens at package init, before
// RunSync has set it), so it's always current for whichever sync run is
// actually in flight.
func fetchLimitRedirectPolicy(req *http.Request, via []*http.Request) error {
	max := currentFetchLimits().MaxRedirectsOrDefault()
	if len(via) > max {
		return fmt.Errorf("%w: more than %d redirects", httplimit.ErrFetchLimitExceeded, max)
	}
	return nil
}

// npmRegistryHTTPClient issues npmRepository's lookup — a var so tests can
// redirect it to a local httptest.Server, matching
// vanityImportHTTPClient's own convention.
var npmRegistryHTTPClient = &http.Client{CheckRedirect: fetchLimitRedirectPolicy}

// npmRegistryDoc is the subset of the npm registry's package document
// this needs. repository can be a plain string ("github:org/repo",
// "org/repo", or a bare URL) or an object — the object shape is tried
// first since json.RawMessage lets both be handled without two round trips.
type npmRegistryDoc struct {
	Repository json.RawMessage `json:"repository"`
}

type npmRepositoryObject struct {
	URL       string `json:"url"`
	Directory string `json:"directory"`
}

// npmShorthandRepo matches npm's "github:org/repo" or bare "org/repo"
// repository-field shorthand.
var npmShorthandRepo = regexp.MustCompile(`^(?:github:)?([\w.-]+)/([\w.-]+?)(?:\.git)?$`)

// githubTreePath matches a GitHub *browse* URL pointing at a ref/path
// within a repo ("/tree/<ref>/<path...>") rather than the repo itself.
// Live-found scanning a real monorepo (mem0): some npm packages'
// repository.url field is set to exactly this convenience link instead
// of the proper clone URL (e.g. @babel/plugin-syntax-object-rest-spread
// → "https://github.com/babel/babel/tree/master/packages/babel-plugin-
// syntax-object-rest-spread") — `git clone --mirror` can't clone a
// browse URL at all, it just fails outright, every time, for every
// version. Stripping it to the bare repo clone URL and letting
// discoverNodeSubdir (already built for REG-012's own monorepo case)
// recover the directory from the tree itself — rather than trying to
// parse a ref/path out of this URL directly — reuses already-tested
// machinery instead of adding a second scoping path.
var githubTreePath = regexp.MustCompile(`^(https://github\.com/[\w.-]+/[\w.-]+?)(?:\.git)?/tree/.*$`)

// npmGitURL normalizes npm's several repository.url shapes
// ("git+https://...", "git://...", "git+ssh://git@...", a bare
// "https://...", or shorthand) to a plain https clone URL.
func npmGitURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if m := npmShorthandRepo.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, "://") {
		return fmt.Sprintf("https://github.com/%s/%s", m[1], m[2]), true
	}
	raw = strings.TrimPrefix(raw, "git+")
	raw = strings.TrimSuffix(raw, ".git")
	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		if m := githubTreePath.FindStringSubmatch(raw); m != nil {
			return m[1], true
		}
		return raw, true
	case strings.HasPrefix(raw, "git://"):
		return "https://" + strings.TrimPrefix(raw, "git://"), true
	case strings.HasPrefix(raw, "git+ssh://git@"), strings.HasPrefix(raw, "ssh://git@"):
		host := strings.TrimPrefix(strings.TrimPrefix(raw, "git+ssh://git@"), "ssh://git@")
		return "https://" + strings.Replace(host, ":", "/", 1), true
	default:
		return "", false
	}
}

// npmRepository looks up name's repository and (if the registry reports a
// monorepo "directory") its subdirectory via the public npm registry —
// the same document `npm view`/`npm install` read. Only a resolvable git
// URL is usable; anything else (no repository field, an unrecognized
// shape, a non-git host) is a clean miss, not an error.
func npmRepository(ctx context.Context, name string) (url, subdir string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, vanityImportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/"+name, nil)
	if err != nil {
		return "", "", false
	}
	resp, err := npmRegistryHTTPClient.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	data, err := httplimit.ReadLimited(resp.Body, currentFetchLimits().MaxFileSizeOrDefault())
	if err != nil {
		return "", "", false
	}
	var doc npmRegistryDoc
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Repository) == 0 {
		return "", "", false
	}

	var obj npmRepositoryObject
	if err := json.Unmarshal(doc.Repository, &obj); err == nil && obj.URL != "" {
		if url, ok := npmGitURL(obj.URL); ok {
			return url, obj.Directory, true
		}
		return "", "", false
	}
	var shorthand string
	if err := json.Unmarshal(doc.Repository, &shorthand); err == nil {
		if url, ok := npmGitURL(shorthand); ok {
			return url, "", true
		}
	}
	return "", "", false
}

// pypiFallbackManifest is REG-013: PyPI has no equivalent of npm's
// registry-reported monorepo "directory" field, so there is no Subdir
// support here at all — a package published from a monorepo subdirectory
// isn't detectable from the PyPI API alone (unlike REG-012's npm case).
// Otherwise the same shape: repository comes from a registry lookup, and
// only a tag verified to actually resolve is ever trusted.
func pypiFallbackManifest(ctx context.Context, dep domain.Dependency) (registry.Manifest, bool) {
	url, ok := pypiRepository(ctx, dep.Name)
	if !ok {
		return registry.Manifest{}, false
	}
	return registry.Manifest{
		Metadata: registry.Metadata{Name: dep.Name},
		Match:    registry.Match{Ecosystems: []domain.Ecosystem{dep.Ecosystem}, Packages: []string{dep.Name}},
		Version:  registry.VersionStrategy{Strategy: "none"},
		Sources: []registry.Source{{
			ID: "repository", Type: "git", URL: url, Authority: 0,
			RefTemplates: []string{"v${version}", "${version}"},
		}},
	}, true
}

// pypiRegistryHTTPClient issues pypiRepository's lookup — a var so tests
// can redirect it to a local httptest.Server, matching
// npmRegistryHTTPClient's own convention.
var pypiRegistryHTTPClient = &http.Client{CheckRedirect: fetchLimitRedirectPolicy}

// pypiRegistryDoc is the subset of PyPI's JSON API response this needs.
type pypiRegistryDoc struct {
	Info struct {
		ProjectURLs map[string]string `json:"project_urls"`
		HomePage    string            `json:"home_page"`
	} `json:"info"`
}

// pypiProjectURLKeys is the priority order to search info.project_urls
// under — verified live, real packages use these keys inconsistently, no
// single one is universal.
var pypiProjectURLKeys = []string{"Source", "Repository", "Source Code", "Code", "GitHub", "Homepage"}

// pypiGitHosts are the hosts pypiGitURL treats as git-hostable — same
// reasoning as REG-012's npm URL normalization, but PyPI's project_urls
// are arbitrary homepage-shaped links (often with extra path segments
// like /issues or /tree/main), not already-canonical clone URLs, so only
// a known host's URL is trusted to mean "this is the repository," not
// just any link a maintainer happened to put in project_urls.
var pypiGitHosts = map[string]bool{"github.com": true, "gitlab.com": true, "bitbucket.org": true}

// pypiGitURL extracts a plain "https://<host>/<org>/<repo>" clone URL from
// an arbitrary project_urls/home_page link, keeping only the first two
// path segments and discarding anything further (issue trackers, file
// paths, branch refs).
func pypiGitURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	if !pypiGitHosts[strings.ToLower(u.Host)] {
		return "", false
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) < 2 || segments[0] == "" || segments[1] == "" {
		return "", false
	}
	org, repo := segments[0], strings.TrimSuffix(segments[1], ".git")
	return fmt.Sprintf("https://%s/%s/%s", u.Host, org, repo), true
}

// pypiRepository looks up name's repository via PyPI's public JSON API —
// the same document `pip show`/PyPI's own web page read. Only a
// recognized git-hostable URL is usable; anything else (no matching
// project_urls key, an unrecognized host) is a clean miss, not an error.
func pypiRepository(ctx context.Context, name string) (url string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, vanityImportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://pypi.org/pypi/"+name+"/json", nil)
	if err != nil {
		return "", false
	}
	resp, err := pypiRegistryHTTPClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	data, err := httplimit.ReadLimited(resp.Body, currentFetchLimits().MaxFileSizeOrDefault())
	if err != nil {
		return "", false
	}
	var doc pypiRegistryDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", false
	}

	for _, key := range pypiProjectURLKeys {
		for k, v := range doc.Info.ProjectURLs {
			if !strings.EqualFold(k, key) {
				continue
			}
			if gitURL, ok := pypiGitURL(v); ok {
				return gitURL, true
			}
		}
	}
	if gitURL, ok := pypiGitURL(doc.Info.HomePage); ok {
		return gitURL, true
	}
	return "", false
}

// moduleTagTemplate is the git tag a Go module's release is published
// under: "v1.2.3" for a module at the repo root, "<subdir>/v1.2.3" for one
// in a subdirectory. Pinning the version's own tag is what makes a
// fallback source version-exact; a branch head would index whatever the
// default branch holds today under every version's label.
func moduleTagTemplate(subdir string) string {
	if subdir == "" {
		return "v${version}"
	}
	return subdir + "/v${version}"
}

// majorVersionSuffix matches a Go module path's trailing /vN major-version
// element (N >= 2), which is part of the import path, not a directory.
var majorVersionSuffix = regexp.MustCompile(`/v([2-9]|[1-9][0-9]+)$`)

// moduleSubdir is where a module lives inside its repository: its path
// below the repo's import root, minus any /vN major-version suffix. A
// monorepo module such as cloud.google.com/go/billing (root
// cloud.google.com/go) owns only "billing/"; a repo-root module owns "".
func moduleSubdir(modulePath, root string) string {
	rest := strings.TrimPrefix(strings.TrimPrefix(modulePath, root), "/")
	return strings.TrimPrefix(majorVersionSuffix.ReplaceAllString("/"+rest, ""), "/")
}

// githubModuleURL is the fast path: a Go module path already directly
// shaped like github.com/<org>/<repo>[/...] needs no lookup at all.
func githubModuleURL(modulePath string) (root, url string, ok bool) {
	segments := strings.Split(modulePath, "/")
	if len(segments) < 3 || segments[0] != "github.com" {
		return "", "", false
	}
	return strings.Join(segments[:3], "/"), "https://github.com/" + segments[1] + "/" + segments[2], true
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
var vanityImportHTTPClient = &http.Client{CheckRedirect: fetchLimitRedirectPolicy}

// resolveVanityImport performs the same lookup `go get` uses for a
// module path with no known VCS host: GET .../<path>?go-get=1 and parse
// the go-import meta tag out of the response. Only a git-VCS result is
// usable — nothing else in ragctl can acquire from a source.
func resolveVanityImport(ctx context.Context, modulePath string) (root, repoURL string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, vanityImportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+modulePath+"?go-get=1", nil)
	if err != nil {
		return "", "", false
	}
	resp, err := vanityImportHTTPClient.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	body, err := httplimit.ReadLimited(resp.Body, currentFetchLimits().MaxFileSizeOrDefault())
	if err != nil {
		return "", "", false
	}

	for _, m := range goImportMetaTag.FindAllStringSubmatch(string(body), -1) {
		fields := strings.Fields(m[1])
		if len(fields) != 3 {
			continue
		}
		importRoot, vcs, url := fields[0], fields[1], fields[2]
		if vcs != "git" {
			continue
		}
		if modulePath == importRoot || strings.HasPrefix(modulePath, importRoot+"/") {
			return importRoot, url, true
		}
	}
	return "", "", false
}

// SyncPhaseTimings is syncVersion's own duration breakdown: build
// (acquisition+normalize+chunk), replicate (embed+upsert), validate, and
// promote — the phase-level counterpart to BuildCoordinator's own
// GateWait/Work split (epic 53/COORD-002). Only phases that actually ran
// before a failure or early return are non-zero.
type SyncPhaseTimings struct {
	Build     time.Duration
	Replicate time.Duration
	Validate  time.Duration
	Promote   time.Duration
}

// latestActiveGenerationAnyVersion returns the most recently promoted
// active generation across every version of one dependency. Deliberately
// named for what it is: it is NOT "the active generation for this
// dependency" (ADR-012 has no such thing) and must never be used to
// decide whether a specific version is built. Only for comparisons
// (VAL-002's sanity baseline) and display (`describe`).
func latestActiveGenerationAnyVersion(ctx context.Context, store *bboltstore.Store, ecosystem domain.Ecosystem, pkg, backendName string) (domain.Generation, bool) {
	pointers, err := store.ListActivePointers(ctx, backendName)
	if err != nil {
		return domain.Generation{}, false
	}
	var latest domain.Generation
	var found bool
	for _, p := range pointers {
		if p.Ecosystem != ecosystem || p.Dependency != pkg {
			continue
		}
		g, err := store.GetGeneration(ctx, p.GenerationID)
		if err != nil {
			continue
		}
		if !found || g.UpdatedAt.After(latest.UpdatedAt) {
			latest, found = g, true
		}
	}
	return latest, found
}

// onSyncPhaseTimings, if set, is called after every syncVersion call
// completes (success or failure) with its phase breakdown. A var, not a
// parameter, so every existing caller/test stays unaffected unless
// something wants to observe it — matches vanityImportHTTPClient's own
// test-injectable-var convention in this file. Tests that set this must
// restore the original value (e.g. via t.Cleanup), since it's shared
// package state.
var onSyncPhaseTimings = func(dep domain.DependencyVersion, t SyncPhaseTimings) {}

// syncVersion drives the full build->replicate->validate->promote
// pipeline for one SYNC_VERSION action.
func syncVersion(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, gitCache *git.Cache, embedder *embedding.Prompted, vb backend.VectorBackend, ns backend.Namespace, reg *registry.Registry, action planner.Action, force bool) error {
	dep := action.Dependency
	var timings SyncPhaseTimings
	defer func() { onSyncPhaseTimings(dep, timings) }()

	manifest, ok := reg.Match(dep.Dependency.Ecosystem, dep.Dependency.Name)
	if !ok {
		manifest, ok = fallbackManifest(ctx, dep.Dependency)
		if !ok {
			// Recorded as its own state, never as a failed generation, so
			// the planner stops retrying it every sync (PLAN-005) while
			// genuinely failed builds keep being retried.
			rec := bboltstore.NoSourceVersion{
				Ecosystem: dep.Dependency.Ecosystem, Package: dep.Dependency.Name, Version: dep.Version,
				Reason: "no registry manifest and no fallback source", RecordedAt: time.Now(),
			}
			if err := store.PutNoSourceVersion(ctx, rec); err != nil {
				return fmt.Errorf("no registry manifest for %s (and recording that failed: %v)", dep.Dependency.Name, err)
			}
			return fmt.Errorf("no registry manifest for %s", dep.Dependency.Name)
		}
	}

	// POINT-004: re-check right before committing to a build. computePlans's
	// own active-generation check (internal/cli/plan.go) is a stale,
	// unlocked snapshot taken before this action was even queued, and
	// BuildCoordinator's singleflight only coalesces callers still in
	// flight together — it does nothing once the first caller has already
	// returned. Two projects resolving the identical dependency+version
	// can each pass computePlans's check and each reach here; re-checking
	// immediately before generation.Create closes that window: if the
	// exact version we're about to build has already been promoted by
	// someone else since we planned, there is nothing left to do.
	if _, err := store.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version, vb.Name()); err == nil {
		return nil
	}
	// VAL-002's sanity check compares this build against a previous one.
	// With several versions active at once (ADR-012), the closest thing
	// to "the previous version" is the most recently promoted generation
	// of any other version of this dependency.
	var prior *domain.Generation
	var priorManifest *generation.Manifest
	if p, ok := latestActiveGenerationAnyVersion(ctx, store, dep.Dependency.Ecosystem, dep.Dependency.Name, vb.Name()); ok {
		prior = &p
		if pm, err := readGenerationManifest(ctx, badgerStore, p.ID); err == nil {
			priorManifest = &pm
		}
	}

	gen, err := generation.Create(ctx, store, badgerStore, dep)
	if err != nil {
		return fmt.Errorf("create generation: %w", err)
	}

	// GIT-004: best-effort — action.ProjectID not resolving to a live
	// project (deleted mid-sync, or a construction path with no real
	// project at all) just means the Node local-cache check can never
	// hit for this build, same as any other miss; never a build failure.
	var projectRoot string
	if proj, err := store.GetProject(ctx, action.ProjectID); err == nil {
		projectRoot = proj.Root
	}

	buildStart := time.Now()
	buildErr := generation.Build(ctx, gen, manifest.Sources, gitCache, store, badgerStore, projectRoot)
	timings.Build = time.Since(buildStart)
	if buildErr != nil {
		return fmt.Errorf("build: %w", buildErr)
	}

	replicateStart := time.Now()
	replicateErr := generation.Replicate(ctx, gen, manifest.Sources, embedder, vb, ns, store, badgerStore)
	timings.Replicate = time.Since(replicateStart)
	if replicateErr != nil {
		return fmt.Errorf("replicate: %w", replicateErr)
	}

	genManifest, err := readGenerationManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	replica, err := store.GetBackendReplica(ctx, gen.ID, vb.Name())
	if err != nil {
		return fmt.Errorf("read replica: %w", err)
	}

	// prior/priorManifest were already fetched above, right before
	// generation.Create, as part of POINT-004's re-check — reused here for
	// validate.Run's version-correctness drift comparison rather than
	// re-fetched, since a build in between wouldn't change which
	// generation was active *before* this one started.
	validateStart := time.Now()
	gen, report, err := validate.Run(ctx, gen, genManifest, replica, prior, priorManifest, validate.DefaultSanityConfig(), embedder, vb, ns, store, badgerStore)
	timings.Validate = time.Since(validateStart)
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

	promoteStart := time.Now()
	promoteErr := promote.Promote(ctx, store, gen, vb.Name(), report.Structural, sanity, report.VersionCorrectness)
	timings.Promote = time.Since(promoteStart)
	if promoteErr != nil {
		return promoteErr
	}
	// A source turned up since an earlier no-source determination (e.g.
	// the registry gained a manifest): the record no longer applies.
	if err := store.DeleteNoSourceVersion(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, dep.Version); err != nil {
		return fmt.Errorf("clear no-source record: %w", err)
	}
	return nil
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

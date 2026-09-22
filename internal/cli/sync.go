package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
	var projectID string
	var dependencies []string
	var dryRun, offline, force bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Execute the sync plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, projectID, dependencies, dryRun, offline, force)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "limit to one project ID")
	cmd.Flags().StringArrayVar(&dependencies, "dependency", nil, "limit to a dependency name (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and exit without executing")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip actions that require network access")
	cmd.Flags().BoolVar(&force, "force", false, "promote a candidate even if VAL-002 sanity thresholds fail (structural/version-correctness failures are never forceable)")
	return cmd
}

func runSync(cmd *cobra.Command, projectID string, dependencies []string, dryRun, offline, force bool) error {
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

	req := api.SyncRequest{ProjectID: projectID, Dependencies: dependencies, Offline: offline, Force: force}
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
//
// coordinator gates every actual generation build (ActionSyncVersion)
// and every reference-state mutation (ActionAddReference/DropReference)
// against concurrent GC and against duplicate builds of the identical
// generation — see daemon.BuildCoordinator and epic 53/COORD-001..002.
func RunSync(ctx context.Context, coordinator *daemon.BuildCoordinator, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, projectID string, dependencies []string, offline, force bool, out io.Writer, readiness *embeddingReadiness, vecReadiness *vectorReadiness, priority *daemon.SyncPriority, progress *daemon.SyncProgress) (synced, failed, skipped int, err error) {
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
	var pipelineMu sync.Mutex
	var pipeline *syncPipeline
	getPipeline := func() (*syncPipeline, error) {
		if err := readiness.checkReady(); err != nil {
			return nil, err
		}
		if err := vecReadiness.checkReady(); err != nil {
			return nil, err
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
	actions := flattenActions(plans, dependencies, out)
	progress.SetTotal(len(actions))

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
				action, ok := queue.next(priority)
				if !ok {
					return
				}
				s, f, sk := runSyncAction(ctx, coordinator, store, badgerStore, reg, getPipeline, action, offline, force, progress, writeLine)
				mu.Lock()
				synced += s
				failed += f
				skipped += sk
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	writeLine("\n%d synced, %d failed, %d skipped\n", synced, failed, skipped)
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
	name := action.Dependency.Dependency.Name
	if action.Kind == planner.ActionSyncVersion {
		progress.Begin(name)
		ctx = generation.WithProgress(ctx, func(done, total int) { progress.SetChunks(name, done, total) })
	}
	defer func() { progress.Finish(name, failed > 0) }()

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
	switch dep.Ecosystem {
	case domain.EcosystemGo:
		return goFallbackManifest(ctx, dep)
	case domain.EcosystemNode:
		return npmFallbackManifest(ctx, dep)
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

// npmRegistryHTTPClient issues npmRepository's lookup — a var so tests can
// redirect it to a local httptest.Server, matching
// vanityImportHTTPClient's own convention.
var npmRegistryHTTPClient = http.DefaultClient

// npmRepositoryField is the subset of the npm registry's package document
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
	var doc npmRegistryDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil || len(doc.Repository) == 0 {
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
var vanityImportHTTPClient = http.DefaultClient

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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
func syncVersion(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, gitCache *git.Cache, embedder embedding.Embedder, vb backend.VectorBackend, ns backend.Namespace, reg *registry.Registry, action planner.Action, force bool) error {
	dep := action.Dependency
	var timings SyncPhaseTimings
	defer func() { onSyncPhaseTimings(dep, timings) }()

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

	buildStart := time.Now()
	buildErr := generation.Build(ctx, gen, manifest.Sources, gitCache, store, badgerStore)
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

	var prior *domain.Generation
	var priorManifest *generation.Manifest
	if p, err := store.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, vb.Name()); err == nil {
		prior = &p
		if pm, err := readGenerationManifest(ctx, badgerStore, p.ID); err == nil {
			priorManifest = &pm
		}
	}

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
	return promoteErr
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

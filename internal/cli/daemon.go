package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon"
	"aleutian-ai/ragctl/internal/daemon/api"
	"aleutian-ai/ragctl/internal/daemon/client"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/export/cognee"
	"aleutian-ai/ragctl/internal/export/graphiti"
	"aleutian-ai/ragctl/internal/export/mem0"
	"aleutian-ai/ragctl/internal/observability"
	"aleutian-ai/ragctl/internal/observability/metrics"
	"aleutian-ai/ragctl/internal/observability/trace"
	"aleutian-ai/ragctl/internal/query"
	"aleutian-ai/ragctl/internal/source/git"
	"aleutian-ai/ragctl/internal/watch"
)

// ragctlVersion identifies this specific binary build, reported by the
// daemon over /v1/health. Previously a hardcoded "v0.1.0" that never
// changed across builds — meaningless for detecting a stale daemon
// (ADR-011: one long-running process reused by every later command),
// since every build reported the exact same string. Go automatically
// stamps VCS revision info into a binary built with `go build` inside a
// git working tree (Go 1.18+, via -buildvcs=auto by default) — reading
// it back via debug.ReadBuildInfo needs no change to any build tooling
// (Makefile, hack/run.sh, CI). Falls back to "unknown" (never "v0.1.0"
// again — a fixed fallback string would silently reintroduce this exact
// bug for any two builds that both hit the fallback) when build info
// genuinely isn't available (e.g. `go run`, or a working tree with no
// VCS), so a mismatch there is real too, just less informative.
var ragctlVersion = detectBuildVersion()

func detectBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "unknown"
	}
	if dirty {
		return revision + "-dirty"
	}
	return revision
}

// stopWait bounds how long `ragctl daemon stop` waits for the socket to
// go away after the daemon accepts the request.
const stopWait = 30 * time.Second

const (
	// autostartTimeout bounds how long a client waits for a daemon it
	// started itself to come up.
	autostartTimeout = 5 * time.Second

	// autostartPoll is how often the client retries the socket while
	// waiting.
	autostartPoll = 100 * time.Millisecond
)

// storeCloseTimeout bounds how long runDaemonRun's shutdown waits for a
// store's Close to return, in closeWithTimeout.
const storeCloseTimeout = 10 * time.Second

// closeWithTimeout runs close and returns once it completes or timeout
// elapses, whichever comes first. A deferred store.Close() blocking
// forever (observed: badgerStore.Close() hanging specifically on the
// auto-start daemon lifecycle, root cause not yet found) would otherwise
// keep the whole daemon process alive indefinitely after Shutdown was
// already requested and the socket already closed — every client-side
// check that the daemon "stopped" only confirms the socket is gone, not
// that the process exited. Writes directly to stderr rather than
// through the daemon's own logf: this is the one path that must still
// report something even if whatever's wrong extends to the daemon's
// normal output machinery.
func closeWithTimeout(name string, close func() error, timeout time.Duration) {
	done := make(chan error, 1)
	go func() { done <- close() }()
	select {
	case err := <-done:
		if err != nil {
			fmt.Fprintf(os.Stderr, "close %s: %v\n", name, err)
		}
	case <-time.After(timeout):
		fmt.Fprintf(os.Stderr, "close %s did not complete within %s; exiting anyway\n", name, timeout)
		os.Exit(1)
	}
}

// daemonExecutable is the binary auto-start spawns. It is empty in
// normal use (meaning "this binary"); tests point it at a real ragctl
// they built, since os.Executable under `go test` is the test binary.
var daemonExecutable string

// ensureDaemon returns a client for the running daemon, starting one if
// none is running and config allows it. Every command that needs stored
// state goes through here: there is deliberately no path that opens the
// stores directly instead (ADR-011).
func ensureDaemon(ctx context.Context) (*client.Client, error) {
	socket, err := socketPath()
	if err != nil {
		return nil, err
	}
	c, err := client.Dial(ctx, socket)
	if err == nil {
		warnIfConfigStale(ctx, c)
		warnIfVersionStale(ctx, c)
		return c, nil
	}
	if !errors.Is(err, client.ErrNotRunning) {
		return nil, err
	}

	cfg, err := loadRagctlConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.Daemon.AutostartEnabled() {
		return nil, notRunningError(socket)
	}
	if err := ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if err := spawnDaemonOnce(socket); err != nil {
		return nil, err
	}
	return waitForDaemon(ctx, socket)
}

// warnIfConfigStale prints a one-line warning to stderr if config.yaml
// has changed since the running daemon loaded it — config is loaded
// once for the daemon's whole lifetime (see docs/internal/daemon.md),
// so an edit doesn't take effect until the daemon is restarted. Best
// effort: any error here is swallowed rather than surfaced, since a
// stale-config warning is a diagnostic nicety, never a reason to fail
// the caller's actual command. Only called for a daemon ensureDaemon
// reused via Dial — one just spawned obviously loaded current config.
func warnIfConfigStale(ctx context.Context, c *client.Client) {
	health, err := c.Health(ctx)
	if err != nil {
		return
	}
	stale, err := configIsStale(health.ConfigFingerprint)
	if err != nil || !stale {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: config.yaml has changed since the running daemon (pid %d) started; the change won't take effect until it restarts — run `ragctl daemon stop` (the next command auto-starts a fresh one)\n", health.PID)
}

// warnIfVersionStale prints a one-line warning to stderr if the running
// daemon (ADR-011: one long-running process, reused by every later
// command) was built from a different revision than the binary making
// this call — e.g. `ragctl` was upgraded (git pull + rebuild, or a new
// release) but the daemon it auto-started earlier is still running the
// old build. A tool/route added since that daemon started genuinely
// doesn't exist on it — an unexplained 404 from a real MCP session is
// exactly this: a live-found symptom, not a hypothetical (live-found
// running a fresh mem0 sync attempt where sync_progress 404'd with no
// obvious cause). Warn only, matching warnIfConfigStale's own
// precedent — auto-restarting here would silently kill whatever the
// stale daemon is mid-sync, which is worse than a confusing error the
// user can act on.
func warnIfVersionStale(ctx context.Context, c *client.Client) {
	health, err := c.Health(ctx)
	if err != nil {
		return
	}
	if msg := versionStaleWarning(health.PID, health.Version, ragctlVersion); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
}

// versionStaleWarning is warnIfVersionStale's pure decision: given the
// running daemon's own reported build version and this command's own
// build version, returns the warning to print, or "" for nothing to
// warn about. "unknown" (build info genuinely unavailable, e.g. `go
// run`) never triggers a warning on its own — comparing two "unknown"s
// would be a guaranteed false negative, but so would treating one real
// "unknown" as automatically stale; there's no reliable signal either
// way, so silence is the honest answer, matching configIsStale's own
// "can't tell, so don't claim to" precedent for its own error path.
func versionStaleWarning(pid int, daemonVersion, currentVersion string) string {
	if daemonVersion == "" || daemonVersion == "unknown" || currentVersion == "unknown" || daemonVersion == currentVersion {
		return ""
	}
	return fmt.Sprintf("warning: the running daemon (pid %d) was built from a different ragctl revision (%s) than this command (%s) — a tool or route added since it started may not exist yet; run `ragctl daemon stop` (the next command auto-starts a fresh one matching this build)", pid, daemonVersion, currentVersion)
}

// configIsStale reports whether config.yaml's current fingerprint
// differs from daemonFingerprint (a Health response's ConfigFingerprint).
func configIsStale(daemonFingerprint string) (bool, error) {
	cfg, err := loadRagctlConfig()
	if err != nil {
		return false, err
	}
	current, err := cfg.Fingerprint()
	if err != nil {
		return false, err
	}
	return current != daemonFingerprint, nil
}

// versionFreshnessLabel is versionStaleWarning's decision rendered for
// `ragctl daemon status`'s "version:" line — the same daemonVersion,
// currentVersion comparison, just formatted for a status line instead
// of a one-shot warning.
func versionFreshnessLabel(daemonVersion string) string {
	if daemonVersion == "" || daemonVersion == "unknown" || ragctlVersion == "unknown" || daemonVersion == ragctlVersion {
		return daemonVersion
	}
	return fmt.Sprintf("%s (stale — this command is %s; run `ragctl daemon stop`)", daemonVersion, ragctlVersion)
}

// configFreshnessLabel is configIsStale rendered for `ragctl daemon
// status`'s "config:" line.
func configFreshnessLabel(daemonFingerprint string) string {
	stale, err := configIsStale(daemonFingerprint)
	if err != nil {
		return "unknown (could not load config.yaml)"
	}
	if stale {
		return "stale (edited since the daemon started; run `ragctl daemon stop`)"
	}
	return "current"
}

// spawnAttempt is one in-flight call to spawnDaemon for a socket: done
// closes once err is safe to read (the write happens-before the close).
type spawnAttempt struct {
	done chan struct{}
	err  error
}

var (
	spawnMu       sync.Mutex
	spawnAttempts = map[string]*spawnAttempt{}
)

// spawnDaemonOnce runs spawnDaemon at most once per socket at a time:
// concurrent ensureDaemon callers in this process (e.g. several commands
// racing to auto-start) share one attempt and its result instead of each
// spawning their own subprocess to race bolt.Open's file lock. That
// race is otherwise real: a losing subprocess can sit blocked in the
// lock wait past the point the winner already answered, and later
// inherit the lock — starting a brand new, unrequested daemon — if the
// winner happens to be told to shut down while it's still waiting. This
// only closes the window for callers sharing one process; a losing
// daemon subprocess itself still relies on openControlStoreForDaemonRun's
// fail-fast timeout to exit before that can happen.
func spawnDaemonOnce(socket string) error {
	spawnMu.Lock()
	if a, ok := spawnAttempts[socket]; ok {
		spawnMu.Unlock()
		<-a.done
		return a.err
	}
	a := &spawnAttempt{done: make(chan struct{})}
	spawnAttempts[socket] = a
	spawnMu.Unlock()

	a.err = spawnDaemon()
	close(a.done)

	spawnMu.Lock()
	delete(spawnAttempts, socket)
	spawnMu.Unlock()
	return a.err
}

// requireNoDaemon fails if a daemon is running, for the few commands
// that open the stores directly and so can't share them.
func requireNoDaemon(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	socket, err := socketPath()
	if err != nil {
		return err
	}
	c, err := client.Dial(ctx, socket)
	if err != nil {
		return nil // not running, or unreachable: nothing owns the stores
	}
	h, err := c.Health(ctx)
	if err != nil {
		return nil
	}
	return fmt.Errorf("the ragctl daemon is running (pid %d); stop it first with `ragctl daemon stop`", h.PID)
}

// notRunningError is what a command reports when there is no daemon and
// it may not start one.
func notRunningError(socket string) error {
	return fmt.Errorf("%w (socket %s); start it with `ragctl daemon run`, or set daemon.autostart: true in config",
		client.ErrNotRunning, socket)
}

// ensureInitialized runs the same work `ragctl init` does, silently
// (status lines to stderr, not stdout — this runs ahead of an auto-start
// a human never explicitly asked for), if the stores don't exist yet.
// Auto-init on first use rather than a hard "run `ragctl init` first"
// refusal: init has no interactive questions, so requiring a manual
// step first serves no purpose except being a surprise blocker for an
// MCP session that has no terminal to run it from — see
// docs/scratch/mcp-bootstrapping.md.
func ensureInitialized(ctx context.Context) error {
	controlPath, err := controlDBPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(controlPath); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := requireNoDaemon(ctx); err != nil {
		return err
	}
	return initStores(os.Stderr)
}

// spawnDaemon starts `ragctl daemon run` detached. Its output goes to
// ragctld.log, never to the caller's stdout: the caller may be `ragctl
// serve`, whose stdout carries the MCP protocol stream.
func spawnDaemon() error {
	exe := daemonExecutable
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("locate the ragctl binary: %w", err)
		}
		// Under `go test` this is the test binary, and spawning it would
		// re-run tests instead of starting a daemon.
		if strings.HasSuffix(filepath.Base(exe), ".test") {
			return fmt.Errorf("refusing to auto-start %s: it is a test binary, not ragctl", exe)
		}
	}
	logPath, err := daemonLogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon", "run")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ragctl daemon: %w", err)
	}
	return cmd.Process.Release()
}

// waitForDaemon polls the socket until the daemon answers, reporting
// what the daemon logged if it never does.
func waitForDaemon(ctx context.Context, socket string) (*client.Client, error) {
	deadline := time.Now().Add(autostartTimeout)
	for {
		c, err := client.Dial(ctx, socket)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, client.ErrNotRunning) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("ragctl daemon did not start within %s; last log lines:\n%s", autostartTimeout, tailDaemonLog())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(autostartPoll):
		}
	}
}

// tailDaemonLog returns the last few lines of ragctld.log, for errors
// that report why an auto-started daemon never came up.
func tailDaemonLog() string {
	path, err := daemonLogPath()
	if err != nil {
		return "(no log available)"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(could not read %s: %v)", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return strings.Join(lines, "\n")
}

// engine implements daemon.Engine over the stores the daemon holds open
// for its lifetime. Every handler's work happens through here, against
// the same functions the CLI used to call directly.
type engine struct {
	store       *bboltstore.Store
	badgerStore *badgerstore.Store
	cfg         config.Config
	controlPath string
	badgerPath  string

	// embeddingReadiness tracks the background check started in
	// runDaemonRun (see embedding_readiness.go) — never built or probed
	// here, only read.
	embeddingReadiness *embeddingReadiness

	// vectorReadiness tracks the background check started in
	// runDaemonRun (see vector_readiness.go) — never built or probed
	// here, only read.
	vectorReadiness *vectorReadiness

	// baseQueryOnce/baseQuery memoize a query.Service backed only by the
	// stores — no embedder or vector backend — for the four methods that
	// never touch those (Status, GetProjectDependencies,
	// GetDependencyVersion, GetReleaseChanges all read only s.control/
	// s.data; see internal/query/search.go). Built on first use, but
	// that's just to keep construction in one place; it makes no network
	// call, so there'd be no real cost to building it at startup either.
	baseQueryOnce sync.Once
	baseQuery     *query.Service

	// fullQueryOnce/fullQuery/fullQueryErr memoize the embedder- and
	// vector-backend-backed query.Service that SearchKnowledge actually
	// needs. Kept separate from baseQuery, and built lazily, specifically
	// so calling Status/ProjectDependencies/DependencyVersion/
	// ReleaseChanges never forces a live embedder dimension probe — only
	// Search does, and only on its first call. Same "no network calls
	// until actually needed" principle RunSync's own lazy pipeline
	// already follows.
	fullQueryOnce sync.Once
	fullQuery     *query.Service
	fullQueryErr  error

	// syncSem is the daemon-wide cap RunSync's daemonSem parameter
	// enforces (SCOPE-002's known risk) — one instance shared by every
	// project's Sync call for this engine's whole lifetime, sized once at
	// construction from cfg.Sync.MaxTotalConcurrency.
	syncSem chan struct{}

	// gitCache is one instance shared by every project's Sync call for
	// this engine's whole lifetime (OPS-007) — RunSync used to build a
	// fresh git.Cache per call, which meant its EnsureMirror in-process
	// mirrorLocks map started empty every time, so it could never
	// actually protect two genuinely concurrent Sync calls (epic 53) that
	// both need the same underlying repository (e.g. two projects on
	// different tags of one monorepo) from racing on the same `git clone
	// --mirror` target directory.
	gitCache *git.Cache
}

// newEngine constructs an engine with its daemon-wide sync semaphore
// sized from cfg — the one place that decides the actual concurrency cap,
// so every construction site (real daemon startup, tests) gets the same
// "0 unmarshals as unset" default-substitution RunSync's own
// MaxConcurrency already establishes, rather than each caller repeating it.
func newEngine(store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, controlPath, badgerPath string, embeddingReadiness *embeddingReadiness, vecReadiness *vectorReadiness) (*engine, error) {
	maxTotal := cfg.Sync.MaxTotalConcurrency
	if maxTotal < 1 {
		maxTotal = 4
	}
	gitCache, err := buildGitCache(cfg)
	if err != nil {
		return nil, fmt.Errorf("build git cache: %w", err)
	}
	return &engine{
		store: store, badgerStore: badgerStore, cfg: cfg, controlPath: controlPath, badgerPath: badgerPath,
		embeddingReadiness: embeddingReadiness, vectorReadiness: vecReadiness,
		syncSem:  make(chan struct{}, maxTotal),
		gitCache: gitCache,
	}, nil
}

// baseQueryService returns e's memoized, stores-only query.Service —
// safe for any method that never touches a vector backend or embedder.
func (e *engine) baseQueryService() *query.Service {
	e.baseQueryOnce.Do(func() {
		e.baseQuery = query.New(e.store, e.badgerStore, nil, nil, backend.Namespace{}, e.cfg.Vector.Backend)
	})
	return e.baseQuery
}

// fullQueryService returns e's memoized, fully-wired query.Service,
// building the embedder and vector backend (and probing embedder
// dimensions) on first use. Only SearchKnowledge needs this.
//
// The readiness check runs every call, outside fullQueryOnce: a
// "still pulling"/"unreachable" result must never be memoized as if it
// were the real build outcome, or every later call would keep
// replaying that stale answer even once the model finishes
// downloading.
func (e *engine) fullQueryService(ctx context.Context) (*query.Service, error) {
	if err := e.embeddingReadiness.checkReady(); err != nil {
		return nil, err
	}
	if err := e.vectorReadiness.checkReady(); err != nil {
		return nil, err
	}
	e.fullQueryOnce.Do(func() {
		vb, err := buildVectorBackend(e.cfg)
		if err != nil {
			e.fullQueryErr = err
			return
		}
		embedder, err := buildEmbedder(e.cfg, e.badgerStore)
		if err != nil {
			e.fullQueryErr = err
			return
		}
		dims, err := embedder.Dimensions(ctx)
		if err != nil {
			e.fullQueryErr = fmt.Errorf("probe embedder dimensions: %w", err)
			return
		}
		ns := backend.Namespace{Name: e.cfg.Vector.Collection, Dimensions: dims, Distance: "cosine"}
		e.fullQuery = query.New(e.store, e.badgerStore, vb, embedder, ns, e.cfg.Vector.Backend)
	})
	return e.fullQuery, e.fullQueryErr
}

// Status returns the `ragctl status` snapshot, including a live backend
// health probe.
func (e *engine) Status(ctx context.Context) (api.Status, error) {
	st, err := buildStatus(ctx, e.store, e.cfg.Vector.Backend, e.controlPath, e.badgerPath)
	if err != nil {
		return api.Status{}, err
	}
	st.Backend = api.BackendStatus{Name: e.cfg.Vector.Backend, Healthy: probeBackend(ctx, e.cfg) == nil}
	return st, nil
}

// Projects returns the projects the daemon watches: every registered
// project with a stored resolution.
func (e *engine) Projects(ctx context.Context) ([]watch.Project, error) {
	return watchProjects(ctx, e.store)
}

// Sync runs one project's sync, re-resolving first when the request came
// from a manifest change. It is the function the scheduler runs, and it
// calls exactly the same RunSync as `ragctl sync`. coordinator is the
// same instance the scheduler uses for GC exclusion — forwarded straight
// into RunSync, which is where the real per-action build coordination
// happens (epic 53/COORD-001..002).
func (e *engine) Sync(ctx context.Context, coordinator *daemon.BuildCoordinator, projectID string, opts daemon.SyncOptions, out io.Writer) (api.SyncResult, error) {
	if opts.Resolve {
		if err := resolveProject(ctx, e.store, projectID, out); err != nil {
			return api.SyncResult{}, err
		}
	}
	synced, failed, skipped, err := RunSync(ctx, coordinator, e.store, e.badgerStore, e.cfg, projectID, opts.Dependencies, opts.Offline, opts.Force, opts.Rebuild, out, e.embeddingReadiness, e.vectorReadiness, opts.Priority, opts.Progress, e.syncSem, e.gitCache)
	return api.SyncResult{ProjectID: projectID, Synced: synced, Failed: failed, Skipped: skipped}, err
}

// EmbeddingReadiness reports the background embedding-provider check's
// current state/detail — see api.Health.EmbeddingState.
func (e *engine) EmbeddingReadiness(ctx context.Context) (state, detail string) {
	s, d := e.embeddingReadiness.get()
	return string(s), d
}

// VectorReadiness reports the background vector-backend check's current
// state/detail — see api.Health.VectorState.
func (e *engine) VectorReadiness(ctx context.Context) (state, detail string) {
	s, d := e.vectorReadiness.get()
	return string(s), d
}

// ProjectIDs returns every registered project, resolved or not — what a
// `ragctl sync` with no --project covers.
func (e *engine) ProjectIDs(ctx context.Context) ([]string, error) {
	projects, err := e.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	return ids, nil
}

// Scan discovers and resolves projects under root, the work behind
// `ragctl scan`.
func (e *engine) Scan(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error) {
	return scanAndResolve(ctx, e.store, root, out, lockProject)
}

// Plan returns the desired-state plan. The result is the CLI's own plan
// type, which the client decodes back into it.
func (e *engine) Plan(ctx context.Context, projectID string) (any, error) {
	return computePlans(ctx, e.store, e.cfg.Vector.Backend, projectID)
}

// GC plans and runs garbage collection.
func (e *engine) GC(ctx context.Context, dryRun bool, out io.Writer) (api.GCResult, error) {
	res, err := RunGC(ctx, e.store, e.badgerStore, e.cfg, dryRun, out, e.vectorReadiness)
	e.reclaimValueLog(dryRun, out)
	return res, err
}

// reclaimValueLog returns the disk space of just-deleted data: Badger
// keeps large values in an append-only log that compaction doesn't
// shrink. A failure only delays reclaiming space, so it is reported, not
// returned.
func (e *engine) reclaimValueLog(dryRun bool, out io.Writer) {
	if dryRun {
		return
	}
	if err := e.badgerStore.ReclaimValueLog(); err != nil {
		fmt.Fprintf(out, "warning: %v\n", err)
	}
}

// OrphanGC plans and runs GC-001/GC-002/GC-003's orphan-generation
// cleanup — see RunOrphanGC.
func (e *engine) OrphanGC(ctx context.Context, dryRun bool, out io.Writer) (api.GCResult, error) {
	res, err := RunOrphanGC(ctx, e.store, e.badgerStore, e.cfg, dryRun, out, e.vectorReadiness)
	e.reclaimValueLog(dryRun, out)
	return res, err
}

// SupersededDuplicatesGC plans and runs POINT-004's cleanup — see
// RunSupersededDuplicatesGC.
func (e *engine) SupersededDuplicatesGC(ctx context.Context, dryRun bool, out io.Writer) (api.GCResult, error) {
	res, err := RunSupersededDuplicatesGC(ctx, e.store, e.badgerStore, e.cfg, dryRun, out, e.vectorReadiness)
	e.reclaimValueLog(dryRun, out)
	return res, err
}

// Search runs a knowledge search, the work behind the search_dependency_docs
// MCP tool.
func (e *engine) Search(ctx context.Context, req api.SearchRequest) (api.SearchResponse, error) {
	svc, err := e.fullQueryService(ctx)
	if err != nil {
		return api.SearchResponse{}, err
	}
	res, err := svc.SearchKnowledge(ctx, query.Query{
		ProjectID:  req.ProjectID,
		Text:       req.Text,
		Dependency: req.Dependency,
		Mode:       query.QueryMode(req.Mode),
		TopK:       req.TopK,
	})
	if err != nil {
		return api.SearchResponse{}, err
	}
	chunks := make([]api.SearchChunk, len(res.Chunks))
	for i, c := range res.Chunks {
		chunks[i] = api.SearchChunk{
			ChunkID:    c.ChunkID,
			Content:    c.Content,
			Score:      c.Score,
			Ecosystem:  c.Ecosystem,
			Dependency: c.Dependency,
			Version:    c.Version,
			Generation: c.Generation,
			SourceType: c.SourceType,
			Authority:  c.Authority,
			TrustClass: string(c.TrustClass),
		}
	}
	return api.SearchResponse{Chunks: chunks}, nil
}

// ProjectDependencies lists a project's resolved dependencies, the work
// behind the list_project_dependencies MCP tool.
func (e *engine) ProjectDependencies(ctx context.Context, projectID string) (api.ProjectDependenciesResponse, error) {
	deps, err := e.baseQueryService().GetProjectDependencies(ctx, projectID)
	if err != nil {
		return api.ProjectDependenciesResponse{}, err
	}
	out := make([]api.ProjectDependency, len(deps))
	for i, d := range deps {
		out[i] = api.ProjectDependency{
			Ecosystem:           string(d.Dependency.Dependency.Ecosystem),
			Name:                d.Dependency.Dependency.Name,
			Direct:              d.Dependency.Dependency.Direct,
			Version:             d.Dependency.Version,
			ResolvedBy:          d.Dependency.ResolvedBy,
			HasActiveGeneration: d.HasActiveGeneration,
		}
	}
	return api.ProjectDependenciesResponse{Dependencies: out}, nil
}

// ExportMem0 pushes projectID's (or req.Dependencies' named subset's)
// already-synced chunks into a user's own Mem0 instance (MEM0-001).
// Read-only against ragctl's own stores — the only state this mutates
// is the user's own external Mem0 instance, and only once a human has
// explicitly run `ragctl export mem0`.
func (e *engine) ExportMem0(ctx context.Context, req api.ExportMem0Request, out io.Writer) (api.ExportMem0Response, error) {
	endpoint := req.Endpoint
	if endpoint == "" {
		endpoint = e.cfg.Export.Mem0.Endpoint
	}
	if endpoint == "" {
		return api.ExportMem0Response{}, fmt.Errorf("mem0 endpoint not configured — set export.mem0.endpoint in config.yaml or pass --endpoint")
	}
	apiKeyEnv := req.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = e.cfg.Export.Mem0.APIKeyEnv
	}
	var apiKey string
	if apiKeyEnv != "" {
		apiKey = os.Getenv(apiKeyEnv)
	}

	mem0Client := mem0.NewClient(endpoint, apiKey)
	if err := mem0Client.Health(ctx); err != nil {
		return api.ExportMem0Response{}, fmt.Errorf("mem0 unreachable at %s: %w", endpoint, err)
	}

	resolution, err := e.store.GetResolution(ctx, req.ProjectID)
	if err != nil {
		return api.ExportMem0Response{}, fmt.Errorf("get resolution for project %s: %w", req.ProjectID, err)
	}

	only := nameSet(req.Dependencies)

	resp := api.ExportMem0Response{}
	for _, dv := range resolution.Dependencies {
		name := dv.Dependency.Name
		if len(only) > 0 && !only[name] {
			continue
		}

		gen, err := e.store.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, name, dv.Version, e.cfg.Vector.Backend)
		if err != nil {
			fmt.Fprintf(out, "%s@%s: not synced yet, skipping (run `ragctl sync`)\n", name, dv.Version)
			continue
		}

		chunks, err := e.badgerStore.ListGenerationChunks(ctx, gen.ID)
		if err != nil {
			return resp, fmt.Errorf("list chunks for %s: %w", name, err)
		}

		result := api.ExportMem0Result{Dependency: name}
		for _, chunk := range chunks {
			obj, err := e.badgerStore.GetKnowledgeObject(ctx, chunk.ObjectID)
			if err != nil {
				result.Failed++
				fmt.Fprintf(out, "%s: get object for chunk %s: %v\n", name, chunk.ID, err)
				continue
			}
			metadata := map[string]string{
				"ecosystem":   string(obj.Dependency.Dependency.Ecosystem),
				"dependency":  obj.Dependency.Dependency.Name,
				"version":     obj.Dependency.Version,
				"generation":  gen.ID,
				"source_type": obj.SourceType,
				"authority":   strconv.Itoa(obj.Authority),
				"trust_class": string(obj.TrustClass),
			}
			if err := mem0Client.AddMemory(ctx, req.ProjectID, string(chunk.Content), metadata); err != nil {
				result.Failed++
				fmt.Fprintf(out, "%s: push chunk %s failed: %v\n", name, chunk.ID, err)
				continue
			}
			result.Pushed++
		}
		fmt.Fprintf(out, "%s: pushed %d, failed %d\n", name, result.Pushed, result.Failed)
		resp.Results = append(resp.Results, result)
	}

	return resp, nil
}

// ExportGraphiti pushes projectID's (or req.Dependencies' named
// subset's) already-synced chunks into a user's own Graphiti instance
// (GRAPHITI-001), one episode per dependency (its full chunk set, not
// one episode per chunk — Graphiti's own extraction pipeline works over
// a coherent document). Read-only against ragctl's own stores.
func (e *engine) ExportGraphiti(ctx context.Context, req api.ExportGraphitiRequest, out io.Writer) (api.ExportGraphitiResponse, error) {
	endpoint := req.Endpoint
	if endpoint == "" {
		endpoint = e.cfg.Export.Graphiti.Endpoint
	}
	if endpoint == "" {
		return api.ExportGraphitiResponse{}, fmt.Errorf("graphiti endpoint not configured — set export.graphiti.endpoint in config.yaml or pass --endpoint")
	}
	authTokenEnv := req.AuthTokenEnv
	if authTokenEnv == "" {
		authTokenEnv = e.cfg.Export.Graphiti.AuthTokenEnv
	}
	var authToken string
	if authTokenEnv != "" {
		authToken = os.Getenv(authTokenEnv)
	}

	graphitiClient := graphiti.NewClient(endpoint, authToken)
	if err := graphitiClient.Health(ctx); err != nil {
		return api.ExportGraphitiResponse{}, fmt.Errorf("graphiti unreachable at %s: %w", endpoint, err)
	}

	resolution, err := e.store.GetResolution(ctx, req.ProjectID)
	if err != nil {
		return api.ExportGraphitiResponse{}, fmt.Errorf("get resolution for project %s: %w", req.ProjectID, err)
	}
	only := nameSet(req.Dependencies)

	resp := api.ExportGraphitiResponse{}
	for _, dv := range resolution.Dependencies {
		name := dv.Dependency.Name
		if len(only) > 0 && !only[name] {
			continue
		}

		gen, err := e.store.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, name, dv.Version, e.cfg.Vector.Backend)
		if err != nil {
			fmt.Fprintf(out, "%s@%s: not synced yet, skipping (run `ragctl sync`)\n", name, dv.Version)
			continue
		}

		episode, err := e.buildGraphitiEpisode(ctx, gen, dv)
		result := api.ExportGraphitiResult{Dependency: name}
		if err != nil {
			result.Failed = 1
			fmt.Fprintf(out, "%s: build episode: %v\n", name, err)
			resp.Results = append(resp.Results, result)
			continue
		}

		if err := graphitiClient.AddEpisode(ctx, req.ProjectID, name, episode); err != nil {
			result.Failed = 1
			fmt.Fprintf(out, "%s: push episode failed: %v\n", name, err)
		} else {
			result.Pushed = 1
			fmt.Fprintf(out, "%s: episode pushed\n", name)
		}
		resp.Results = append(resp.Results, result)
	}
	return resp, nil
}

// graphitiEpisode is one dependency's chunks, shaped per GRAPHITI-001's
// design — serialized as one episode's content.
type graphitiEpisode struct {
	Dependency string                 `json:"dependency"`
	Version    string                 `json:"version"`
	Ecosystem  string                 `json:"ecosystem"`
	Chunks     []graphitiEpisodeChunk `json:"chunks"`
}

type graphitiEpisodeChunk struct {
	Content    string `json:"content"`
	SourceType string `json:"source_type"`
	TrustClass string `json:"trust_class"`
	Breadcrumb string `json:"breadcrumb,omitempty"`
}

// buildGraphitiEpisode reads gen's chunks and their parent objects,
// shared by ExportGraphiti and (via the same badger reads) nothing
// else — kept here rather than in the graphiti package itself, since
// only the daemon-side engine touches Badger (ADR-011).
func (e *engine) buildGraphitiEpisode(ctx context.Context, gen domain.Generation, dv domain.DependencyVersion) (graphitiEpisode, error) {
	chunks, err := e.badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		return graphitiEpisode{}, fmt.Errorf("list chunks: %w", err)
	}
	episode := graphitiEpisode{
		Dependency: dv.Dependency.Name,
		Version:    dv.Version,
		Ecosystem:  string(dv.Dependency.Ecosystem),
	}
	for _, chunk := range chunks {
		obj, err := e.badgerStore.GetKnowledgeObject(ctx, chunk.ObjectID)
		if err != nil {
			return graphitiEpisode{}, fmt.Errorf("get object for chunk %s: %w", chunk.ID, err)
		}
		episode.Chunks = append(episode.Chunks, graphitiEpisodeChunk{
			Content:    string(chunk.Content),
			SourceType: obj.SourceType,
			TrustClass: string(obj.TrustClass),
			Breadcrumb: strings.Join(obj.Heading, " > "),
		})
	}
	return episode, nil
}

// ExportCognee pushes projectID's (or req.Dependencies' named subset's)
// already-synced chunks into a user's own Cognee instance (COGNEE-001):
// one file per dependency added to a dataset named for the project ID,
// then a single cognify call over the whole dataset once every add has
// completed.
func (e *engine) ExportCognee(ctx context.Context, req api.ExportCogneeRequest, out io.Writer) (api.ExportCogneeResponse, error) {
	endpoint := req.Endpoint
	if endpoint == "" {
		endpoint = e.cfg.Export.Cognee.Endpoint
	}
	if endpoint == "" {
		return api.ExportCogneeResponse{}, fmt.Errorf("cognee endpoint not configured — set export.cognee.endpoint in config.yaml or pass --endpoint")
	}
	authTokenEnv := req.AuthTokenEnv
	if authTokenEnv == "" {
		authTokenEnv = e.cfg.Export.Cognee.AuthTokenEnv
	}
	var authToken string
	if authTokenEnv != "" {
		authToken = os.Getenv(authTokenEnv)
	}

	cogneeClient := cognee.NewClient(endpoint, authToken)
	if err := cogneeClient.Health(ctx); err != nil {
		return api.ExportCogneeResponse{}, fmt.Errorf("cognee unreachable at %s: %w", endpoint, err)
	}

	resolution, err := e.store.GetResolution(ctx, req.ProjectID)
	if err != nil {
		return api.ExportCogneeResponse{}, fmt.Errorf("get resolution for project %s: %w", req.ProjectID, err)
	}
	only := nameSet(req.Dependencies)

	resp := api.ExportCogneeResponse{}
	var addedAny bool
	for _, dv := range resolution.Dependencies {
		name := dv.Dependency.Name
		if len(only) > 0 && !only[name] {
			continue
		}

		gen, err := e.store.GetActiveGeneration(ctx, dv.Dependency.Ecosystem, name, dv.Version, e.cfg.Vector.Backend)
		if err != nil {
			fmt.Fprintf(out, "%s@%s: not synced yet, skipping (run `ragctl sync`)\n", name, dv.Version)
			continue
		}

		episode, err := e.buildGraphitiEpisode(ctx, gen, dv) // same aggregated-chunks shape works for Cognee's own file upload
		result := api.ExportCogneeResult{Dependency: name}
		if err != nil {
			result.Failed = 1
			fmt.Fprintf(out, "%s: build content: %v\n", name, err)
			resp.Results = append(resp.Results, result)
			continue
		}
		content, err := json.Marshal(episode)
		if err != nil {
			result.Failed = 1
			fmt.Fprintf(out, "%s: encode content: %v\n", name, err)
			resp.Results = append(resp.Results, result)
			continue
		}

		if err := cogneeClient.Add(ctx, req.ProjectID, name+".json", content); err != nil {
			result.Failed = 1
			fmt.Fprintf(out, "%s: add failed: %v\n", name, err)
		} else {
			result.Pushed = 1
			addedAny = true
			fmt.Fprintf(out, "%s: added\n", name)
		}
		resp.Results = append(resp.Results, result)
	}

	if addedAny {
		const defaultCogneeChunkSize = 1024
		if err := cogneeClient.Cognify(ctx, req.ProjectID, defaultCogneeChunkSize); err != nil {
			resp.CognifyError = err.Error()
			fmt.Fprintf(out, "cognify failed: %v\n", err)
		} else {
			fmt.Fprintln(out, "cognify triggered")
		}
	}
	return resp, nil
}

// nameSet builds a lookup set from a dependency-name filter list; an
// empty list means "every dependency" (len(set) == 0 is the caller's
// own check for that).
func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// DependencyVersion resolves one package's version within a project, the
// work behind the get_dependency_version MCP tool.
func (e *engine) DependencyVersion(ctx context.Context, projectID, pkg string) (api.DependencyVersionResponse, error) {
	dv, err := e.baseQueryService().GetDependencyVersion(ctx, projectID, pkg)
	if err != nil {
		return api.DependencyVersionResponse{}, err
	}
	return api.DependencyVersionResponse{
		Ecosystem:  string(dv.Dependency.Ecosystem),
		Name:       dv.Dependency.Name,
		Direct:     dv.Dependency.Direct,
		Version:    dv.Version,
		ResolvedBy: dv.ResolvedBy,
		Checksum:   dv.Checksum,
	}, nil
}

// ReleaseChanges gets release-note excerpts between two versions of a
// dependency, the work behind the get_release_changes MCP tool.
func (e *engine) ReleaseChanges(ctx context.Context, dependency, from, to string) (api.ReleaseChangesResponse, error) {
	changes, err := e.baseQueryService().GetReleaseChanges(ctx, dependency, from, to)
	if err != nil {
		return api.ReleaseChangesResponse{}, err
	}
	out := make([]api.ReleaseChange, len(changes))
	for i, c := range changes {
		out[i] = api.ReleaseChange{Ecosystem: c.Ecosystem, Version: c.Version, Excerpt: c.Excerpt}
	}
	return api.ReleaseChangesResponse{Changes: out}, nil
}

// KnowledgeStatus summarizes fleet-wide sync coverage, the work behind
// the knowledge_status MCP tool.
func (e *engine) KnowledgeStatus(ctx context.Context) (api.KnowledgeStatusResponse, error) {
	st, err := e.baseQueryService().Status(ctx)
	if err != nil {
		return api.KnowledgeStatusResponse{}, err
	}
	projects := make([]api.ProjectRef, len(st.Projects))
	for i, p := range st.Projects {
		projects[i] = api.ProjectRef{ID: p.ID, Root: p.Root}
	}
	return api.KnowledgeStatusResponse{
		TotalProjects:           st.TotalProjects,
		TotalDependencies:       st.TotalDependencies,
		WithActiveGeneration:    st.WithActiveGeneration,
		WithoutActiveGeneration: st.WithoutActiveGeneration,
		Projects:                projects,
	}, nil
}

// ProjectList lists every registered project, the work behind
// `ragctl project list`.
func (e *engine) ProjectList(ctx context.Context) (api.ProjectListResponse, error) {
	projects, err := e.store.ListProjects(ctx)
	if err != nil {
		return api.ProjectListResponse{}, fmt.Errorf("list projects: %w", err)
	}
	out := make([]api.ProjectSummary, len(projects))
	for i, p := range projects {
		out[i] = api.ProjectSummary{ID: p.ID, Root: p.Root}
	}
	return api.ProjectListResponse{Projects: out}, nil
}

// ProjectGet gets one project's full detail, the work behind
// `ragctl project show` and `ragctl deps`.
func (e *engine) ProjectGet(ctx context.Context, projectID string) (api.ProjectGetResponse, error) {
	// The friendly message is built here, not left to the caller to
	// construct from an error type: an HTTP error crossing the daemon
	// socket is a plain string (api.Error), not a wrapped Go error a
	// client could errors.Is against bboltstore.ErrNotFound.
	p, err := e.store.GetProject(ctx, projectID)
	if errors.Is(err, bboltstore.ErrNotFound) {
		return api.ProjectGetResponse{}, fmt.Errorf("no registered project with ID %s (run `ragctl project list` to see registered projects)", projectID)
	}
	if err != nil {
		return api.ProjectGetResponse{}, fmt.Errorf("get project %s: %w", projectID, err)
	}
	resp := api.ProjectGetResponse{ID: p.ID, Root: p.Root, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}

	res, err := e.store.GetResolution(ctx, projectID)
	if errors.Is(err, bboltstore.ErrNotFound) {
		return resp, nil
	}
	if err != nil {
		return api.ProjectGetResponse{}, fmt.Errorf("get resolution for %s: %w", projectID, err)
	}
	resp.HasResolution = true
	resp.Ecosystem = string(res.Ecosystem)
	resp.Fingerprint = res.Fingerprint
	resp.Dependencies = make([]api.DependencyInfo, len(res.Dependencies))
	for i, d := range res.Dependencies {
		resp.Dependencies[i] = api.DependencyInfo{
			Ecosystem: string(d.Dependency.Ecosystem),
			Name:      d.Dependency.Name,
			Version:   d.Version,
			Direct:    d.Dependency.Direct,
		}
	}
	return resp, nil
}

// Describe builds the fleet-wide (or filtered) knowledge report, the
// work behind `ragctl describe`.
func (e *engine) Describe(ctx context.Context, args []string, checkLiveness bool) (any, error) {
	reg, err := loadRegistryForCLI(ctx)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	var filterPairs []depPair
	switch len(args) {
	case 1:
		filterPairs, err = aliasPairs(reg, args[0])
		if err != nil {
			return nil, err
		}
	case 2:
		filterPairs = []depPair{{ecosystem: domain.Ecosystem(args[0]), pkg: args[1]}}
	}

	return buildReport(ctx, e.store, e.badgerStore, reg, e.cfg.Vector.Backend, filterPairs, checkLiveness)
}

// doctorChecksInDaemon is every doctorChecks entry except the last two
// (git on PATH, package managers on PATH) — the ones that must run
// against the calling user's own shell PATH, never the daemon's. A
// slice, not a name filter, since doctorChecks is a small, hand-authored
// literal with those two already last; doctor_test.go's own ordering
// tests would catch a reorder that broke this assumption.
var doctorChecksInDaemon = doctorChecks[:len(doctorChecks)-2]

// Doctor runs every doctor check that needs this daemon's stores, the
// work behind `ragctl doctor` when a daemon is reachable. The stores are
// already open and config/registry already loaded, so doctorEnv here
// carries no error states the way the no-daemon fallback path's does.
func (e *engine) Doctor(ctx context.Context) (api.DoctorResponse, error) {
	reg, regErr := loadRegistryForCLI(ctx)
	env := &doctorEnv{
		cfg:                e.cfg,
		store:              e.store,
		badger:             e.badgerStore,
		registry:           reg,
		registryErr:        regErr,
		now:                time.Now(),
		embeddingReadiness: e.embeddingReadiness,
		vectorReadiness:    e.vectorReadiness,
	}

	checks := make([]api.CheckResultWire, len(doctorChecksInDaemon))
	for i, c := range doctorChecksInDaemon {
		sev, detail := c.run(ctx, env)
		checks[i] = api.CheckResultWire{Name: c.name, Severity: int(sev), Detail: detail}
	}

	needed, sev, detail, ok := packageManagersNeeded(ctx, env)
	if !ok {
		// The one check that determines what executables are needed
		// failed outright (not just "found none") — report it as its own
		// check entry rather than silently omitting NeededExecutables.
		checks = append(checks, api.CheckResultWire{Name: "package managers on PATH", Severity: int(sev), Detail: detail})
		return api.DoctorResponse{Checks: checks}, nil
	}
	neededWire := make([]api.NeededExecutable, 0, len(needed))
	for exe, count := range needed {
		neededWire = append(neededWire, api.NeededExecutable{Name: exe, Count: count})
	}
	return api.DoctorResponse{Checks: checks, NeededExecutables: neededWire}, nil
}

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the ragctl daemon",
		Long: `The ragctl daemon owns ragctl's databases. Every command that needs
stored state talks to it over a local socket, and starts one if none is
running (unless daemon.autostart is false in config).`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "run",
			Short: "Run the ragctl daemon in the foreground",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return runDaemonRun(cmd) },
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the ragctl daemon is running",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return runDaemonStatus(cmd) },
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the running ragctl daemon",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return runDaemonStop(cmd) },
		},
	)
	return cmd
}

func runDaemonRun(cmd *cobra.Command) error {
	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}
	configFingerprint, err := cfg.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint config: %w", err)
	}
	socket, err := socketPath()
	if err != nil {
		return err
	}
	controlPath, err := controlDBPath()
	if err != nil {
		return err
	}
	badgerPath, err := badgerDirPath()
	if err != nil {
		return err
	}

	// Ownership of the control store, not the socket file, decides who is
	// the daemon (ADR-011): a crash leaves a socket behind, but never the
	// lock.
	store, err := openControlStoreForDaemonRun()
	if err != nil {
		if errors.Is(err, bboltstore.ErrLocked) {
			return ownershipError(cmd.Context(), socket, controlPath)
		}
		return fmt.Errorf("open control store: %w", err)
	}
	defer closeWithTimeout("control store", store.Close, storeCloseTimeout)

	badgerStore, err := openDataStore()
	if err != nil {
		return fmt.Errorf("open data store: %w", err)
	}
	defer closeWithTimeout("data store", badgerStore.Close, storeCloseTimeout)

	out := &lockedWriter{w: cmd.OutOrStdout()}
	logf := func(format string, args ...any) {
		fmt.Fprintf(out, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
	}

	// OBS-001: the daemon's whole lifetime runs under one context carrying
	// one structured logger — every function further down any call chain
	// rooted here (sync, gc, query, ...) gets it via
	// observability.FromContext without needing its own *slog.Logger
	// parameter.
	logger := observability.NewLogger(out, cfg.Log.JSON, cfg.Log.SlogLevel())
	ctx, stop := signal.NotifyContext(observability.WithLogger(context.Background(), logger), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default handling, so a second Ctrl-C exits immediately
	}()

	// OBS-002: off by default (cfg.Observability.OTel.Enabled is false
	// unless explicitly set) — see internal/observability/trace.
	// shutdownTracing flushes any pending spans; a failed exporter never
	// blocks or fails daemon startup (InitProvider's own contract).
	shutdownTracing, err := trace.InitProvider(ctx, cfg.Observability.OTel, logger)
	if err != nil {
		return fmt.Errorf("init tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Warn("otel: tracer shutdown failed", "error", err)
		}
	}()

	// OBS-003: off by default (cfg.Observability.Metrics.Enabled is false
	// unless explicitly set) — see internal/observability/metrics. A bind
	// failure is fatal only because metrics were explicitly requested;
	// StartServer itself is a no-op (nil error) when disabled.
	shutdownMetrics, err := metrics.StartServer(cfg.Observability.Metrics, logger)
	if err != nil {
		return fmt.Errorf("init metrics: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownMetrics(shutdownCtx); err != nil {
			logger.Warn("metrics: server shutdown failed", "error", err)
		}
	}()
	if cfg.Observability.Metrics.Enabled {
		go refreshStorageMetrics(ctx, store, cfg.Vector.Backend, badgerPath, storageMetricsRefreshInterval)
	}

	// Checked off the request path entirely (WATCH-014): a client that
	// needs an embedder (sync, search) gets an immediate, actionable
	// "still pulling"/"unreachable" answer from embeddingReadiness
	// instead of triggering — and blocking on — a live pull itself.
	readiness := newEmbeddingReadiness()
	if cfg.Embedding.Provider == "ollama" {
		readiness.reprobe = ollamaReprober(ctx, cfg, readiness, logf)
	}
	go checkEmbeddingReadiness(ctx, cfg, readiness, logf)

	// Same off-request-path treatment for the vector backend (WATCH-015):
	// a client that needs Qdrant (sync, GC, search) gets an immediate,
	// actionable "unreachable" answer from vectorReadiness instead of a
	// raw dial error, once per dependency, from deep inside
	// generation.Replicate.
	vecReadiness := newVectorReadiness()
	vecReadiness.reprobe = func() error { return probeBackend(ctx, cfg) }
	go checkVectorReadiness(ctx, cfg, vecReadiness, logf)

	// SAFE-001 (epic 61): warn once, at startup, if this fresh instance's
	// configured collection already holds data it never wrote — see
	// checkForeignCollectionData's own doc comment for why this is a
	// warning, never a refusal.
	go checkForeignCollectionData(ctx, cfg, store, logf)

	engine, err := newEngine(store, badgerStore, cfg, controlPath, badgerPath, readiness, vecReadiness)
	if err != nil {
		return err
	}
	srv := daemon.New(daemon.Options{
		Engine:             engine,
		Socket:             socket,
		ControlPath:        controlPath,
		Version:            ragctlVersion,
		WatchEnabled:       cfg.Watch.Enabled,
		DisableAmbientSync: cfg.Sync.DisableAmbient,
		Debounce:           cfg.Watch.Debounce,
		MCPEnabled:         cfg.Server.MCP.Enabled,
		EnableSyncTool:     cfg.Server.MCP.EnableSyncTool,
		ConfigFingerprint:  configFingerprint,
		Logf:               logf,
		Out:                out,
	})
	if err := srv.Serve(ctx); err != nil {
		return err
	}
	logf("stopped")
	return nil
}

// ownershipError explains a locked control store: either another daemon
// is already running (the normal outcome of two commands auto-starting
// at once), or something else holds the lock and nothing answers.
func ownershipError(ctx context.Context, socket, controlPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if h, err := client.Dial(probe, socket); err == nil {
		health, err := h.Health(probe)
		if err == nil {
			return fmt.Errorf("ragctl daemon already running (pid %d, socket %s)", health.PID, socket)
		}
	}
	return fmt.Errorf("%s is locked but no daemon answers on %s; another ragctl process holds it", controlPath, socket)
}

func runDaemonStatus(cmd *cobra.Command) error {
	socket, err := socketPath()
	if err != nil {
		return err
	}
	c, err := client.Dial(cmd.Context(), socket)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			fmt.Fprintf(cmd.OutOrStdout(), "ragctl daemon is not running (socket %s)\n", socket)
			return ExitCodeError{Code: 1}
		}
		return err
	}
	h, err := c.Health(cmd.Context())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%-14s running (pid %d)\n", "daemon:", h.PID)
	fmt.Fprintf(out, "%-14s %s\n", "socket:", h.Socket)
	fmt.Fprintf(out, "%-14s %s\n", "control db:", h.ControlPath)
	fmt.Fprintf(out, "%-14s %s\n", "version:", versionFreshnessLabel(h.Version))
	fmt.Fprintf(out, "%-14s %s\n", "uptime:", time.Since(h.StartedAt).Truncate(time.Second))
	fmt.Fprintf(out, "%-14s %t\n", "watching:", h.Watching)
	fmt.Fprintf(out, "%-14s %s\n", "config:", configFreshnessLabel(h.ConfigFingerprint))
	fmt.Fprintf(out, "%-14s %s\n", "embedding:", embeddingStatusLabel(h.EmbeddingState, h.EmbeddingDetail))
	fmt.Fprintf(out, "%-14s %s\n", "vector:", vectorStatusLabel(h.VectorState, h.VectorDetail))
	return nil
}

// embeddingStatusLabel formats a daemon's reported embedding-readiness
// state for `ragctl daemon status`'s one-line summary.
func embeddingStatusLabel(state, detail string) string {
	switch embeddingState(state) {
	case embeddingStateReady, embeddingStateUnknown, "":
		return "ready"
	case embeddingStateChecking:
		return "checking"
	case embeddingStatePulling:
		return fmt.Sprintf("pulling %s", detail)
	case embeddingStateUnreachable:
		return fmt.Sprintf("unreachable (%s)", detail)
	case embeddingStateError:
		return fmt.Sprintf("error: %s", detail)
	default:
		return state
	}
}

// vectorStatusLabel formats a daemon's reported vector-readiness state
// for `ragctl daemon status`'s one-line summary.
func vectorStatusLabel(state, detail string) string {
	switch vectorState(state) {
	case vectorStateReady, vectorStateUnknown, "":
		return "ready"
	case vectorStateChecking:
		return "checking"
	case vectorStateStarting:
		return fmt.Sprintf("starting managed container %s", detail)
	case vectorStateUnreachable:
		return fmt.Sprintf("unreachable (%s)", detail)
	case vectorStateError:
		return fmt.Sprintf("error: %s", detail)
	default:
		return state
	}
}

func runDaemonStop(cmd *cobra.Command) error {
	socket, err := socketPath()
	if err != nil {
		return err
	}
	c, err := client.Dial(cmd.Context(), socket)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			fmt.Fprintln(cmd.OutOrStdout(), "ragctl daemon is not running")
			return nil
		}
		return err
	}

	// OPS-006: remember the daemon's real PID before shutting it down.
	// The socket stops accepting connections as soon as the HTTP
	// listener's own graceful shutdown completes (bounded by
	// shutdownGrace, ~30s) — which can happen well before the process
	// itself actually exits: Server.Serve doesn't return until
	// Scheduler.Wait() does, and any sync/GC already in flight keeps
	// running underneath that, bounded only by its own per-run
	// maxActionDuration (30m), decoupled from the daemon's own shutdown
	// signal (by design — see Scheduler's context.WithoutCancel(s.base)).
	// Without this PID check, the loop below declares "stopped" the
	// moment the *socket* is gone, which is exactly the false-positive
	// STRESS-017 found live. A failed Health call degrades to the
	// pre-existing socket-only behavior rather than erroring — never
	// worse than before this fix.
	pid := 0
	if h, err := c.Health(cmd.Context()); err == nil {
		pid = h.PID
	}

	if err := c.Shutdown(cmd.Context()); err != nil {
		return err
	}

	deadline := time.Now().Add(stopWait)
	for time.Now().Before(deadline) {
		if _, err := client.Dial(cmd.Context(), socket); errors.Is(err, client.ErrNotRunning) {
			// The socket necessarily closes strictly before the process
			// finishes its own teardown (the final store Close() calls
			// run after Server.Serve returns) — even with nothing ever
			// in flight, there's a real, normally-brief window here.
			// waitForProcessExit absorbs exactly that expected lag
			// before deciding whether this is "just finishing teardown"
			// (report "stopped") or "genuinely still running a long
			// sync/GC" (report the honest still-finishing message).
			stopped := waitForProcessExit(pid, processExitGrace)
			if stopped {
				fmt.Fprintln(cmd.OutOrStdout(), "stopped")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), stopOutcomeMessage(pid))
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not stop within %s", stopWait)
}

// processExitGrace is how long waitForProcessExit tolerates after the
// daemon's socket closes before concluding the process is genuinely
// still busy (as opposed to just finishing its own ordinary teardown) —
// short relative to the up-to-30-minute in-flight-work ceiling this
// check exists to catch, per OPS-006.
const processExitGrace = 2 * time.Second

// waitForProcessExit polls pid (via processAlive) for up to grace,
// returning true as soon as it's no longer alive, false if it's still
// alive once grace elapses. A pid of 0 (Health call failed earlier)
// always reports true — degrades to the pre-existing socket-only
// behavior rather than blocking on a check that was never possible.
func waitForProcessExit(pid int, grace time.Duration) bool {
	if pid == 0 {
		return true
	}
	deadline := time.Now().Add(grace)
	for {
		if !processAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stopOutcomeMessage decides what `ragctl daemon stop` tells the user
// once the socket is confirmed gone — split out from runDaemonStop so
// the decision itself (not the socket-polling loop around it) is
// directly unit-testable without a real daemon.
//
// `ragctl daemon status` would misreport "not running" in the
// still-finishing case too (it dials the same socket), so the message
// points at `ps` instead — the one check that's actually accurate in
// this window.
func stopOutcomeMessage(pid int) string {
	if pid == 0 || !processAlive(pid) {
		return "stopped"
	}
	return fmt.Sprintf(
		"shutdown requested; daemon (pid %d) is finishing an in-flight sync/GC before it exits on its own (bounded to 30m) — check with `ps -p %d`",
		pid, pid)
}

// processAlive reports whether pid names a live process, via a signal-0
// liveness probe (sends no actual signal — Unix-standard way to check
// existence/permission without affecting the process). False for pid <=
// 0 or any error (including "no such process"), never a false positive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// lockedWriter serializes writes from the daemon's goroutines onto one
// output stream.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

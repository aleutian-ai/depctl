package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// ErrShuttingDown is delivered to requests the daemon will never run
// because it is stopping.
var ErrShuttingDown = errors.New("ragctl daemon is shutting down")

// maxActionDuration bounds one GC run (and the scan and export routes),
// so a hung network call can never keep the daemon busy forever. GC holds
// the build gate exclusively (BuildCoordinator.ExcludeForGC), so an
// unbounded GC would stall every build. Sync runs aren't wrapped in it:
// each SYNC_VERSION action has its own bound instead (see execute). A var
// only so tests can shorten it.
var maxActionDuration = 30 * time.Minute

// SyncOptions are the knobs one sync run takes. Resolve is set by
// watch-triggered requests, which re-resolve the project first; a plain
// `ragctl sync` leaves it false and syncs the stored resolution.
type SyncOptions struct {
	// Dependencies limits the run to exactly these dependency names; empty
	// or nil means everything.
	Dependencies []string
	Offline      bool
	Force        bool
	// Rebuild (OPS-004) clears each named Dependency's active-generation
	// pointer and version reference before planning, forcing a genuine
	// rebuild regardless of the planner's own version-unchanged NOOP
	// branch — see RunSync's own doc comment for why Force alone can't do
	// this. Meaningless (ignored) if Dependencies is empty.
	Rebuild bool
	Resolve bool
	// Priority is set by Scheduler.start for every run it launches
	// (never by a caller of Request) — RunSync consults it between
	// actions to let a concurrent BumpPriority call reorder this run's
	// remaining queue (WATCH-020). Always non-nil for a
	// Scheduler-launched run; nil for anything constructed directly
	// (tests, and any future caller that bypasses the Scheduler) — a
	// nil *SyncPriority is a valid, always-empty no-op.
	Priority *SyncPriority
	// Progress is set by Scheduler.start alongside Priority, for the same
	// reason: RunSync updates it as actions finish and SyncProgress reads
	// it back (SCOPE-001). Nil for anything constructed directly.
	Progress *SyncProgress
}

// SyncPriority is a small FIFO of dependency names a concurrent caller
// wants prioritized within an already-running sync (WATCH-020) — see
// Scheduler.BumpPriority. Nil-receiver-safe throughout, so a caller that
// never sets one (SyncOptions.Priority left nil) pays nothing extra.
type SyncPriority struct {
	mu      sync.Mutex
	pending []string
}

// Bump adds dependency to the queue of names RunSync should move to the
// front of its remaining work the next time it checks.
func (p *SyncPriority) Bump(dependency string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = append(p.pending, dependency)
}

// Drain returns every pending bump and clears it — a cheap, non-blocking
// poll RunSync's action loop calls between actions, not a channel read
// (that loop runs synchronously; there's no natural place to block on a
// channel between one dependency's sync finishing and the next one
// starting).
func (p *SyncPriority) Drain() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := p.pending
	p.pending = nil
	return pending
}

// SyncFunc runs one sync to completion, writing progress to out.
// coordinator is the same instance Scheduler uses for GC exclusion
// (ExcludeForGC) — RunSync is expected to route each actual generation
// build through coordinator.Build, and each reference-state mutation
// through coordinator.ProtectFromGC, since PlanGC reads that same state
// directly (see COORD-001/002).
type SyncFunc func(ctx context.Context, coordinator *BuildCoordinator, projectID string, opts SyncOptions, out io.Writer) (api.SyncResult, error)

// GCFunc runs one GC pass to completion, writing progress to out.
type GCFunc func(ctx context.Context, dryRun bool, out io.Writer) (api.GCResult, error)

// Result is the outcome of the sync run that covered one request.
type Result struct {
	Sync api.SyncResult
	Err  error
}

// GCOutcome is the outcome of the GC run that covered one request.
type GCOutcome struct {
	GC  api.GCResult
	Err error
}

// Scheduler runs syncs one per project and GC daemon-wide, collapsing
// requests that arrive while the same work is already running into a
// single follow-up run. It holds no durable state: work pending when the
// daemon stops is lost, and the next file change or command asks for it
// again (ADR-011 §7).
type Scheduler struct {
	base  context.Context
	run   SyncFunc
	runGC GCFunc
	logf  func(format string, args ...any)

	// coordinator replaces what used to be a single global sync.Mutex
	// (see epic 53/COORD-001..002): GC still needs excluding from every
	// concurrent build and reference-state mutation (ADR-011 §6, PlanGC
	// reads the same `references`/`active_generations` state a build can
	// be actively mutating), but unrelated builds no longer need to
	// exclude *each other* — only builds of the identical generation do,
	// via coordinator.Build's singleflight coalescing. execute() no
	// longer takes any lock of its own at all; RunSync (via SyncFunc's
	// coordinator param) does the real per-action coordination.
	coordinator *BuildCoordinator

	mu       sync.Mutex
	projects map[string]*projectState
	gc       gcState
	stopped  bool
	inFlight sync.WaitGroup

	// scanMu/scanLocks serialize scanAndResolve's persist step per
	// project — see LockProject. Independent of the sync/GC machinery
	// above: scanning doesn't touch generations or references, so it
	// only needs to keep two concurrent scans of the *same* project from
	// racing each other's PutProject/PutResolution, not the global lock.
	scanMu    sync.Mutex
	scanLocks map[string]*scanLock
}

// gcState is GC's place in the state machine — the same idle/running/
// running-with-a-change-queued shape as projectState, but daemon-wide
// (one instance, not one per key) since GC always considers every
// project's retention state at once, never just one project's.
type gcState struct {
	running bool
	// queue holds follow-up runs in arrival order: requests with the
	// same dryRun collapse into one; a real request and a dry run never
	// do, so a preview can't delete and a real GC can't be downgraded.
	queue []queuedGC
}

// queuedGC is one follow-up GC run and the callers it will answer.
type queuedGC struct {
	dryRun  bool
	waiters []*gcWaiter
}

// gcWaiter is one caller's interest in the next GC run: where its
// progress output goes, and where its result is delivered.
type gcWaiter struct {
	out  io.Writer
	done chan GCOutcome
}

// queuedSync is one follow-up sync run and the callers it will answer.
type queuedSync struct {
	opts    SyncOptions
	waiters []*waiter
}

// projectState is one project's place in the state machine: idle (absent
// or !syncing), syncing, and syncing with a change already queued.
type projectState struct {
	syncing bool
	// queue holds follow-up runs in arrival order. A request merges into
	// a queued run it's compatible with (see compatible); otherwise it
	// queues its own, so a rebuild is never lost by merging into a plain
	// sync, or vice versa.
	queue []queuedSync
	// priority is the currently-running sync's live SyncPriority, set by
	// start and cleared by finish — nil whenever no sync is running for
	// this project, which is exactly what BumpPriority checks.
	priority *SyncPriority
	// progress is the *currently in-flight* run's live counters — start
	// replaces it with a fresh object every time a new run begins, so it
	// is only meaningful while syncing is true.
	progress *SyncProgress
	// lastMeaningful is the most recent *meaningful* run's final outcome
	// (Total > 0 — it actually planned and attempted something), updated
	// only in finish(), never touched by start(). MCP-004: a run that
	// finds nothing to do (Total == 0 — the common, correct case once
	// everything is already synced) leaves this alone rather than
	// clobbering a real prior failure with empty counters just because
	// it happened to start and finish first. SyncProgress reads this
	// (not a fresh progress object) once syncing is false.
	lastMeaningful api.SyncProgress
	// everRan is set unconditionally in finish(), regardless of
	// Total — unlike lastMeaningful, it must not stay false just
	// because the most recent run found nothing new to sync. It's the
	// only thing that distinguishes "synced, nothing to do" from
	// "never synced" once lastMeaningful is still its zero value.
	everRan bool
}

// waiter is one caller's interest in the next run for a project: where
// its progress output goes, and where its result is delivered.
type waiter struct {
	out  io.Writer
	done chan Result
}

// NewScheduler returns a Scheduler that runs syncs with run and GC with
// runGC. Runs use a context derived from base but not cancelled with it,
// so a run already under way finishes instead of leaving a half-built
// generation.
func NewScheduler(base context.Context, run SyncFunc, runGC GCFunc, logf func(string, ...any)) *Scheduler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Scheduler{
		base:        base,
		run:         run,
		runGC:       runGC,
		logf:        logf,
		coordinator: NewBuildCoordinator(),
		projects:    map[string]*projectState{},
		scanLocks:   map[string]*scanLock{},
	}
}

// Request queues a sync for projectID and returns a channel that
// receives the result of the run covering this request — the run it
// starts, or the single follow-up run if one is already under way.
// Progress is written to out, which may be nil.
func (s *Scheduler) Request(projectID string, opts SyncOptions, out io.Writer) <-chan Result {
	w := &waiter{out: out, done: make(chan Result, 1)}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		w.done <- Result{Err: ErrShuttingDown}
		return w.done
	}

	st := s.projects[projectID]
	if st == nil {
		st = &projectState{}
		s.projects[projectID] = st
	}
	if st.syncing {
		for i := range st.queue {
			if compatible(st.queue[i].opts, opts) {
				st.queue[i].opts = mergeOptions(st.queue[i].opts, opts)
				st.queue[i].waiters = append(st.queue[i].waiters, w)
				return w.done
			}
		}
		st.queue = append(st.queue, queuedSync{opts: opts, waiters: []*waiter{w}})
		return w.done
	}

	st.syncing = true
	s.start(projectID, opts, []*waiter{w})
	return w.done
}

// RequestOrphanGC runs one manual, non-coalesced GC pass — named for its
// original caller (GC-001/GC-002's orphan-generation cleanup), but
// generic enough in shape that POINT-004's superseded-duplicate cleanup
// reuses it unchanged too, passing Engine.SupersededDuplicatesGC as run
// instead. Excluded from every concurrent build/reference-mutation via
// the same coordinator.ExcludeForGC executeGC's runs already use.
// Unlike RequestGC, this has none of its request-coalescing
// sophistication — reference-based GC needs that because file-watch
// events can fire sync (and therefore, indirectly, interest in GC)
// repeatedly in quick succession, but every caller of this method is
// deliberately manual/opt-in (epic 22's own non-goal for orphan GC: "no
// automatic/implicit orphan cleanup" — POINT-004's cleanup is the same
// kind of deliberate, explicit-trigger-only operation), so a caller
// always gets its own real run and its own real result rather than
// being folded into someone else's. run is GCFunc-shaped, passed in
// rather than stored on Scheduler so this method needs no change to
// NewScheduler's constructor or its existing tests for either caller.
func (s *Scheduler) RequestOrphanGC(ctx context.Context, run GCFunc, dryRun bool, out io.Writer) (api.GCResult, error) {
	if out == nil {
		out = io.Discard // matches writerForGC's own nil-safety for RequestGC's callers
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return api.GCResult{}, ErrShuttingDown
	}
	s.inFlight.Add(1)
	s.mu.Unlock()
	defer s.inFlight.Done()

	release := s.coordinator.ExcludeForGC()
	defer release()

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(s.base), maxActionDuration)
	defer cancel()
	return run(runCtx, dryRun, out)
}

// RequestGC queues a GC run and returns a channel that receives the
// result of the run covering this request — the run it starts, or the
// single follow-up run if one is already under way. Progress is written
// to out, which may be nil.
func (s *Scheduler) RequestGC(dryRun bool, out io.Writer) <-chan GCOutcome {
	w := &gcWaiter{out: out, done: make(chan GCOutcome, 1)}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		w.done <- GCOutcome{Err: ErrShuttingDown}
		return w.done
	}

	if s.gc.running {
		for i := range s.gc.queue {
			if s.gc.queue[i].dryRun == dryRun {
				s.gc.queue[i].waiters = append(s.gc.queue[i].waiters, w)
				return w.done
			}
		}
		s.gc.queue = append(s.gc.queue, queuedGC{dryRun: dryRun, waiters: []*gcWaiter{w}})
		return w.done
	}

	s.gc.running = true
	s.startGC(dryRun, []*gcWaiter{w})
	return w.done
}

// LockProject blocks until it can exclusively hold projectID's scan
// lock, returning the func that releases it. scanAndResolve holds this
// around one project's persist step, so two concurrent scans that
// discover the same project serialize instead of racing each other's
// PutProject/PutResolution.
//
// scanLocks entries are refcounted and removed once nothing holds or
// wants them: a daemon that's scanned N distinct projects over its
// lifetime keeps at most as many entries as are *currently* contended,
// not one per project ever scanned.
func (s *Scheduler) LockProject(projectID string) func() {
	s.scanMu.Lock()
	l, ok := s.scanLocks[projectID]
	if !ok {
		l = &scanLock{}
		s.scanLocks[projectID] = l
	}
	l.refs++
	s.scanMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.scanMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.scanLocks, projectID)
		}
		s.scanMu.Unlock()
	}
}

// scanLock is one project's scan lock plus how many callers currently
// hold or are waiting for it, so LockProject's caller can tell when it's
// safe to remove the entry entirely instead of keeping it forever.
type scanLock struct {
	mu   sync.Mutex
	refs int
}

// States reports each project's current scheduler state, for `ragctl
// status`.
func (s *Scheduler) States() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make(map[string]string, len(s.projects))
	for id, st := range s.projects {
		switch {
		case st.syncing && len(st.queue) > 0:
			states[id] = "syncing+dirty"
		case st.syncing:
			states[id] = "syncing"
		default:
			states[id] = "idle"
		}
	}
	return states
}

// Shutdown stops accepting work and fails everything still queued. Runs
// already under way are left to finish; Wait blocks for them.
func (s *Scheduler) Shutdown() {
	s.mu.Lock()
	s.stopped = true
	for _, st := range s.projects {
		for _, q := range st.queue {
			deliver(q.waiters, Result{Err: ErrShuttingDown})
		}
		st.queue = nil
	}
	for _, q := range s.gc.queue {
		deliverGC(q.waiters, GCOutcome{Err: ErrShuttingDown})
	}
	s.gc.queue = nil
	s.mu.Unlock()
}

// Wait blocks until no sync or GC run is in flight.
func (s *Scheduler) Wait() { s.inFlight.Wait() }

// start launches one run. Callers hold s.mu already (Request, finish),
// so setting st.priority here is safe without a separate lock.
func (s *Scheduler) start(projectID string, opts SyncOptions, waiters []*waiter) {
	priority := &SyncPriority{}
	progress := &SyncProgress{}
	if st := s.projects[projectID]; st != nil {
		st.priority = priority
		st.progress = progress
	}
	opts.Priority = priority
	opts.Progress = progress

	s.inFlight.Add(1)
	go func() {
		defer s.inFlight.Done()
		result := s.execute(projectID, opts, waiters)
		deliver(waiters, result)
		s.finish(projectID)
	}()
}

// SyncingProjects returns the progress of every project with a sync in
// flight right now, keyed by project ID.
func (s *Scheduler) SyncingProjects() map[string]api.SyncProgress {
	s.mu.Lock()
	running := map[string]*SyncProgress{}
	for id, st := range s.projects {
		if st.syncing {
			running[id] = st.progress
		}
	}
	s.mu.Unlock()

	out := make(map[string]api.SyncProgress, len(running))
	for id, p := range running {
		snap := p.Snapshot()
		snap.Syncing = true
		snap.Ran = true
		out[id] = snap
	}
	return out
}

// SyncProgress reports projectID's in-flight sync — or, when none is
// running, the last *meaningful* run's final counters (all zero if
// nothing has ever actually attempted work for this project). MCP-004:
// deliberately reads lastMeaningful, not a fresh/just-replaced progress
// object, once syncing is false — see projectState's own doc comment.
// A cheap read with no side effects.
func (s *Scheduler) SyncProgress(projectID string) api.SyncProgress {
	s.mu.Lock()
	st := s.projects[projectID]
	var snap api.SyncProgress
	syncing := false
	ran := false
	if st != nil {
		syncing = st.syncing
		ran = st.everRan
		if syncing {
			snap = st.progress.Snapshot()
		} else {
			snap = st.lastMeaningful
		}
	}
	s.mu.Unlock()

	snap.Syncing = syncing
	snap.Ran = ran || syncing // a currently-running first sync also counts as "ran"
	return snap
}

// BumpPriority asks the currently-running sync for projectID, if any,
// to move dependency to the front of its remaining work the next time
// it checks (WATCH-020). Returns false when no sync is currently
// running for projectID — the caller (a JIT single-dependency sync
// request, WATCH-019) falls back to its own plain Request in that case.
func (s *Scheduler) BumpPriority(projectID, dependency string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.projects[projectID]
	if st == nil || !st.syncing || st.priority == nil {
		return false
	}
	st.priority.Bump(dependency)
	return true
}

// execute runs one sync, turning a panic into an error so one project
// can never take the daemon down with it.
func (s *Scheduler) execute(projectID string, opts SyncOptions, waiters []*waiter) (result Result) {
	defer func() {
		if r := recover(); r != nil {
			result = Result{Err: fmt.Errorf("sync %s panicked: %v", projectID, r)}
			s.logf("%s: %v", projectID, result.Err)
		}
	}()

	// No lock taken here at all, deliberately (epic 53/COORD-002): two
	// different projects' runs no longer exclude each other just for
	// existing — only builds of the identical generation coalesce
	// (coordinator.Build), and GC excludes every build/mutation via
	// coordinator.ExcludeForGC/ProtectFromGC, both inside s.run/RunSync
	// itself now, not wrapped around the whole call here.
	//
	// No aggregate maxActionDuration wrap here either, as of epic 53/
	// COORD-003 — deliberately, not an oversight. STRESS-005 found this
	// ceiling was the wrong granularity for a large untargeted sync: one
	// shared deadline for hundreds of dependencies meant a handful of
	// genuinely slow ones could starve everything queued behind them.
	// The real protection against "a hung network call keeps this
	// goroutine alive forever" already exists at the right granularity
	// without it: every individual network/subprocess call in the sync
	// pipeline already carries its own bound (ollama's and qdrant's own
	// http.Client.Timeout, executil.Run's per-call Timeout), and
	// RunSync's own dependencySyncTimeout now bounds each
	// SYNC_VERSION action specifically. context.WithoutCancel still
	// deliberately survives Shutdown (a sync already under way must
	// finish, not leave a half-built generation — see the type doc).
	ctx := context.WithoutCancel(s.base)
	out := writerFor(waiters)
	res, err := s.run(ctx, s.coordinator, projectID, opts, out)
	if err != nil {
		s.logf("%s: %v", projectID, err)
	}
	return Result{Sync: res, Err: err}
}

// finish moves the project out of "syncing": straight to idle, or into
// the single follow-up run that collapsed changes arriving mid-sync.
func (s *Scheduler) finish(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.projects[projectID]
	if st == nil {
		return
	}
	// MCP-004: capture the run that's *just now* finishing before
	// deciding what happens next — unconditionally, not only on the
	// "truly idle" path below. The dirty-coalescing branch immediately
	// starts a follow-up run without ever setting st.syncing = false in
	// between (by design — a queued change means the project was never
	// really idle), so putting this capture after that branch's early
	// return meant a coalesced follow-up silently discarded whatever
	// real result the run it's replacing just produced — live-found:
	// exactly this sequence (a real, failing ambient sync, immediately
	// followed by a coalesced no-op request) is what produced the
	// original bug report's stale "0 failed" result in the first place.
	if snap := st.progress.Snapshot(); snap.Total > 0 {
		st.lastMeaningful = snap
	}
	// Unconditional, unlike lastMeaningful above: a run that found
	// nothing new to sync still really ran (see everRan's own doc
	// comment on projectState).
	st.everRan = true
	if len(st.queue) > 0 && !s.stopped {
		next := st.queue[0]
		st.queue = st.queue[1:]
		s.start(projectID, next.opts, next.waiters)
		return
	}
	st.syncing = false
	st.priority = nil
	if s.stopped {
		for _, q := range st.queue {
			deliver(q.waiters, Result{Err: ErrShuttingDown})
		}
		st.queue = nil
	}
}

// startGC launches one GC run. The caller holds s.mu and has already
// marked GC as running. Mirrors start.
func (s *Scheduler) startGC(dryRun bool, waiters []*gcWaiter) {
	s.inFlight.Add(1)
	go func() {
		defer s.inFlight.Done()
		result := s.executeGC(dryRun, waiters)
		deliverGC(waiters, result)
		s.finishGC()
	}()
}

// executeGC runs one GC pass, turning a panic into an error so GC can
// never take the daemon down with it. coordinator.ExcludeForGC waits for
// every currently in-flight build/reference-mutation across every
// project before this runs, and blocks new ones from starting until
// release is called (epic 53/COORD-001..002) — replacing what used to
// be the same global sync.Mutex execute() took. Same maxActionDuration
// ceiling and "survives shutdown, doesn't run forever" reasoning as
// execute().
func (s *Scheduler) executeGC(dryRun bool, waiters []*gcWaiter) (result GCOutcome) {
	defer func() {
		if r := recover(); r != nil {
			result = GCOutcome{Err: fmt.Errorf("gc panicked: %v", r)}
			s.logf("gc: %v", result.Err)
		}
	}()

	release := s.coordinator.ExcludeForGC()
	defer release()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), maxActionDuration)
	defer cancel()
	out := writerForGC(waiters)
	res, err := s.runGC(ctx, dryRun, out)
	if err != nil {
		s.logf("gc: %v", err)
	}
	return GCOutcome{GC: res, Err: err}
}

// finishGC moves GC out of "running": straight to idle, or into the
// single follow-up run that collapsed requests arriving mid-run. Mirrors
// finish.
func (s *Scheduler) finishGC() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.gc.queue) > 0 && !s.stopped {
		next := s.gc.queue[0]
		s.gc.queue = s.gc.queue[1:]
		s.startGC(next.dryRun, next.waiters)
		return
	}
	s.gc.running = false
	if s.stopped {
		for _, q := range s.gc.queue {
			deliverGC(q.waiters, GCOutcome{Err: ErrShuttingDown})
		}
		s.gc.queue = nil
	}
}

// compatible reports whether two sync requests can share one run. A
// rebuild applies only to its named dependencies, so merging it with a
// plain sync would either narrow the other caller's sync to those names
// or lose the rebuild; rebuilds only merge with rebuilds.
func compatible(a, b SyncOptions) bool {
	return a.Rebuild == b.Rebuild
}

// mergeOptions folds a request into a compatible queued run: anything
// one caller asked to force or re-resolve happens, offline only survives
// if every caller wanted it, and dependency filters union — but a caller
// with no filter wants everything, which absorbs any named set.
func mergeOptions(pending, next SyncOptions) SyncOptions {
	merged := SyncOptions{
		Offline: pending.Offline && next.Offline,
		Force:   pending.Force || next.Force,
		Rebuild: pending.Rebuild && next.Rebuild,
		Resolve: pending.Resolve || next.Resolve,
	}
	// A rebuild only means something for named dependencies, so for
	// rebuilds the names always combine; otherwise no filter absorbs any.
	if merged.Rebuild || len(pending.Dependencies) > 0 && len(next.Dependencies) > 0 {
		merged.Dependencies = unionNames(pending.Dependencies, next.Dependencies)
	}
	return merged
}

// unionNames returns the sorted, de-duplicated union of a and b.
func unionNames(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, name := range append(append([]string{}, a...), b...) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func deliver(waiters []*waiter, r Result) {
	for _, w := range waiters {
		select {
		case w.done <- r:
		default: // buffered, and each waiter is delivered to once
		}
	}
}

// writerFor fans one run's progress out to every caller waiting on it.
func writerFor(waiters []*waiter) io.Writer {
	var writers []io.Writer
	for _, w := range waiters {
		if w.out != nil {
			writers = append(writers, w.out)
		}
	}
	if len(writers) == 0 {
		return io.Discard
	}
	return io.MultiWriter(writers...)
}

func deliverGC(waiters []*gcWaiter, r GCOutcome) {
	for _, w := range waiters {
		select {
		case w.done <- r:
		default: // buffered, and each waiter is delivered to once
		}
	}
}

// writerForGC fans one GC run's progress out to every caller waiting on it.
func writerForGC(waiters []*gcWaiter) io.Writer {
	var writers []io.Writer
	for _, w := range waiters {
		if w.out != nil {
			writers = append(writers, w.out)
		}
	}
	if len(writers) == 0 {
		return io.Discard
	}
	return io.MultiWriter(writers...)
}

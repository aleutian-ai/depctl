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

// maxActionDuration bounds one sync or GC run so a hung network call (an
// unreachable embedder or vector backend) can never keep the daemon
// process alive forever — see execute's own comment for why this
// doesn't conflict with "never cancel an in-flight run." Shared by both:
// GC takes the same global lock sync does (see gcState's doc), so an
// unbounded GC would wedge every future sync exactly as an unbounded
// sync used to wedge every future GC. A var only so tests can shorten it.
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
	Resolve      bool
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
	dirty   bool
	pending bool // dry-run only if every collapsed request wanted dry-run
	waiters []*gcWaiter
}

// gcWaiter is one caller's interest in the next GC run: where its
// progress output goes, and where its result is delivered.
type gcWaiter struct {
	out  io.Writer
	done chan GCOutcome
}

// projectState is one project's place in the state machine: idle (absent
// or !syncing), syncing, and syncing with a change already queued.
type projectState struct {
	syncing bool
	dirty   bool
	pending SyncOptions
	waiters []*waiter
	// priority is the currently-running sync's live SyncPriority, set by
	// start and cleared by finish — nil whenever no sync is running for
	// this project, which is exactly what BumpPriority checks.
	priority *SyncPriority
	// progress is the current run's counters, kept after the run ends so
	// SyncProgress can report the last run's outcome.
	progress *SyncProgress
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
		if st.dirty {
			st.pending = mergeOptions(st.pending, opts)
		} else {
			st.dirty = true
			st.pending = opts
		}
		st.waiters = append(st.waiters, w)
		return w.done
	}

	st.syncing = true
	s.start(projectID, opts, []*waiter{w})
	return w.done
}

// RequestOrphanGC runs one orphan GC pass (GC-001/GC-002), excluded from
// every concurrent build/reference-mutation via the same
// coordinator.ExcludeForGC executeGC's runs already use. Unlike
// RequestGC, this has none of its request-coalescing sophistication —
// reference-based GC needs that
// because file-watch events can fire sync (and therefore, indirectly,
// interest in GC) repeatedly in quick succession, but orphan GC is
// deliberately manual/opt-in (epic 22's own non-goal: "no automatic/
// implicit orphan cleanup"), so a caller always gets its own real run
// and its own real result rather than being folded into someone else's.
// run is GCFunc-shaped but orphan-specific (GC-002's runOrphanGC),
// passed in rather than stored on Scheduler so this method needs no
// change to NewScheduler's constructor or its existing tests.
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
		if s.gc.dirty {
			// OR, not AND: a caller who explicitly asked for a preview must
			// never have that silently escalate into a real delete just
			// because it collapsed with someone else's non-dry-run request.
			// The reverse (a real request folded into a dry-run, so nothing
			// gets deleted) is the safe direction to err in — recoverable by
			// asking again, unlike a surprise deletion.
			s.gc.pending = s.gc.pending || dryRun
		} else {
			s.gc.dirty = true
			s.gc.pending = dryRun
		}
		s.gc.waiters = append(s.gc.waiters, w)
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
		case st.syncing && st.dirty:
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
		st.dirty = false
		deliver(st.waiters, Result{Err: ErrShuttingDown})
		st.waiters = nil
	}
	s.gc.dirty = false
	deliverGC(s.gc.waiters, GCOutcome{Err: ErrShuttingDown})
	s.gc.waiters = nil
	s.mu.Unlock()
}

// Wait blocks until no sync or GC run is in flight.
func (s *Scheduler) Wait() { s.inFlight.Wait() }

// start launches one run. The caller holds s.mu and has already marked
// the project as syncing.
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
		out[id] = snap
	}
	return out
}

// SyncProgress reports projectID's in-flight sync — or, when none is
// running, its last run's final counters (all zero if it never synced).
// A cheap read with no side effects.
func (s *Scheduler) SyncProgress(projectID string) api.SyncProgress {
	s.mu.Lock()
	st := s.projects[projectID]
	var progress *SyncProgress
	syncing := false
	if st != nil {
		progress, syncing = st.progress, st.syncing
	}
	s.mu.Unlock()

	snap := progress.Snapshot()
	snap.Syncing = syncing
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
	if st.dirty && !s.stopped {
		st.dirty = false
		opts, waiters := st.pending, st.waiters
		st.pending, st.waiters = SyncOptions{}, nil
		s.start(projectID, opts, waiters)
		return
	}
	st.syncing = false
	st.priority = nil
	if s.stopped && len(st.waiters) > 0 {
		deliver(st.waiters, Result{Err: ErrShuttingDown})
		st.waiters = nil
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

	if s.gc.dirty && !s.stopped {
		s.gc.dirty = false
		dryRun, waiters := s.gc.pending, s.gc.waiters
		s.gc.pending, s.gc.waiters = false, nil
		s.startGC(dryRun, waiters)
		return
	}
	s.gc.running = false
	if s.stopped && len(s.gc.waiters) > 0 {
		deliverGC(s.gc.waiters, GCOutcome{Err: ErrShuttingDown})
		s.gc.waiters = nil
	}
}

// mergeOptions folds a request into the pending follow-up: anything one
// caller asked to force or re-resolve happens, offline only survives if
// every caller wanted it, and dependency filters union — but a caller
// with no filter wants everything, which absorbs any named set.
func mergeOptions(pending, next SyncOptions) SyncOptions {
	merged := SyncOptions{
		Offline: pending.Offline && next.Offline,
		Force:   pending.Force || next.Force,
		Resolve: pending.Resolve || next.Resolve,
	}
	if len(pending.Dependencies) > 0 && len(next.Dependencies) > 0 {
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

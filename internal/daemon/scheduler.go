package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// ErrShuttingDown is delivered to sync requests that the daemon will
// never run because it is stopping.
var ErrShuttingDown = errors.New("ragctl daemon is shutting down")

// SyncOptions are the knobs one sync run takes. Resolve is set by
// watch-triggered requests, which re-resolve the project first; a plain
// `ragctl sync` leaves it false and syncs the stored resolution.
type SyncOptions struct {
	Dependency string
	Offline    bool
	Force      bool
	Resolve    bool
}

// SyncFunc runs one sync to completion, writing progress to out.
type SyncFunc func(ctx context.Context, projectID string, opts SyncOptions, out io.Writer) (api.SyncResult, error)

// Result is the outcome of the run that covered one request.
type Result struct {
	Sync api.SyncResult
	Err  error
}

// Scheduler runs syncs one per project, collapsing requests that arrive
// while a project is already syncing into a single follow-up run. It
// holds no durable state: work pending when the daemon stops is lost,
// and the next file change or command asks for it again (ADR-011 §7).
type Scheduler struct {
	base context.Context
	run  SyncFunc
	logf func(format string, args ...any)

	// global serializes runs across projects for v1: generations are
	// shared between projects, so two projects could otherwise race to
	// build the same one (ADR-011 §6). Per-project state above is
	// deliberately independent of this, so dropping the restriction later
	// means removing this lock, not reworking the state machine.
	global sync.Mutex

	mu       sync.Mutex
	projects map[string]*projectState
	stopped  bool
	inFlight sync.WaitGroup
}

// projectState is one project's place in the state machine: idle (absent
// or !syncing), syncing, and syncing with a change already queued.
type projectState struct {
	syncing bool
	dirty   bool
	pending SyncOptions
	waiters []*waiter
}

// waiter is one caller's interest in the next run for a project: where
// its progress output goes, and where its result is delivered.
type waiter struct {
	out  io.Writer
	done chan Result
}

// NewScheduler returns a Scheduler that runs syncs with run. Runs use a
// context derived from base but not cancelled with it, so a sync already
// under way finishes instead of leaving a half-built generation.
func NewScheduler(base context.Context, run SyncFunc, logf func(string, ...any)) *Scheduler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Scheduler{base: base, run: run, logf: logf, projects: map[string]*projectState{}}
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
	s.mu.Unlock()
}

// Wait blocks until no sync is running.
func (s *Scheduler) Wait() { s.inFlight.Wait() }

// start launches one run. The caller holds s.mu and has already marked
// the project as syncing.
func (s *Scheduler) start(projectID string, opts SyncOptions, waiters []*waiter) {
	s.inFlight.Add(1)
	go func() {
		defer s.inFlight.Done()
		result := s.execute(projectID, opts, waiters)
		deliver(waiters, result)
		s.finish(projectID)
	}()
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

	s.global.Lock()
	defer s.global.Unlock()

	out := writerFor(waiters)
	res, err := s.run(context.WithoutCancel(s.base), projectID, opts, out)
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
	if s.stopped && len(st.waiters) > 0 {
		deliver(st.waiters, Result{Err: ErrShuttingDown})
		st.waiters = nil
	}
}

// mergeOptions folds a request into the pending follow-up: anything one
// caller asked to force or re-resolve happens, offline only survives if
// every caller wanted it, and a dependency filter survives only while
// every caller named the same one.
func mergeOptions(pending, next SyncOptions) SyncOptions {
	merged := SyncOptions{
		Offline: pending.Offline && next.Offline,
		Force:   pending.Force || next.Force,
		Resolve: pending.Resolve || next.Resolve,
	}
	if pending.Dependency == next.Dependency {
		merged.Dependency = pending.Dependency
	}
	return merged
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

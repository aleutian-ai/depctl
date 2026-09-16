package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// fakeSync is a sync function whose runs are gated by channels, so a
// test can hold one "mid-sync" while it queues more requests.
type fakeSync struct {
	mu       sync.Mutex
	calls    []call
	started  chan string
	release  chan struct{}
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	fail     func(projectID string, n int) error
}

type call struct {
	projectID string
	opts      SyncOptions
}

func newFakeSync() *fakeSync {
	return &fakeSync{started: make(chan string, 32), release: make(chan struct{})}
}

func (f *fakeSync) run(_ context.Context, projectID string, opts SyncOptions, out io.Writer) (api.SyncResult, error) {
	n := f.record(projectID, opts)
	now := f.inFlight.Add(1)
	for {
		seen := f.maxSeen.Load()
		if now <= seen || f.maxSeen.CompareAndSwap(seen, now) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	f.started <- projectID
	fmt.Fprintf(out, "syncing %s\n", projectID)
	<-f.release

	if f.fail != nil {
		if err := f.fail(projectID, n); err != nil {
			return api.SyncResult{}, err
		}
	}
	return api.SyncResult{ProjectID: projectID, Synced: 1}, nil
}

// record appends the call and returns how many times this project has
// been synced, counting this one.
func (f *fakeSync) record(projectID string, opts SyncOptions) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{projectID, opts})
	n := 0
	for _, c := range f.calls {
		if c.projectID == projectID {
			n++
		}
	}
	return n
}

func (f *fakeSync) snapshot() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeSync) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a sync to start")
	}
}

func awaitResult(t *testing.T, ch <-chan Result) Result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a sync result")
		return Result{}
	}
}

// fakeGC mirrors fakeSync for GC: its runs are gated by a channel, so a
// test can hold one "mid-run" while it queues more requests or checks
// what else is (or isn't) allowed to run concurrently with it.
type fakeGC struct {
	mu       sync.Mutex
	calls    []bool // one entry per call, its dryRun value
	started  chan struct{}
	release  chan struct{}
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	fail     error
}

func newFakeGC() *fakeGC {
	return &fakeGC{started: make(chan struct{}, 32), release: make(chan struct{})}
}

func (f *fakeGC) run(_ context.Context, dryRun bool, out io.Writer) (api.GCResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dryRun)
	f.mu.Unlock()

	now := f.inFlight.Add(1)
	for {
		seen := f.maxSeen.Load()
		if now <= seen || f.maxSeen.CompareAndSwap(seen, now) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	f.started <- struct{}{}
	fmt.Fprintf(out, "gc\n")
	<-f.release

	if f.fail != nil {
		return api.GCResult{}, f.fail
	}
	return api.GCResult{Candidates: 1, Deleted: 1, DryRun: dryRun}, nil
}

func (f *fakeGC) snapshot() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.calls...)
}

func (f *fakeGC) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a gc run to start")
	}
}

func awaitGCResult(t *testing.T, ch <-chan GCOutcome) GCOutcome {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a gc result")
		return GCOutcome{}
	}
}

// noGC is a GCFunc for tests that only exercise sync — never expected to
// be called.
func noGC(context.Context, bool, io.Writer) (api.GCResult, error) {
	panic("gc should not run in this test")
}

func TestSchedulerCollapsesRequestsIntoOneFollowUp(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	first := s.Request("proj_a", SyncOptions{}, nil)
	f.awaitStart(t)

	// Five more changes arrive while the first run is still going.
	var queued []<-chan Result
	for range 5 {
		queued = append(queued, s.Request("proj_a", SyncOptions{}, nil))
	}
	if states := s.States(); states["proj_a"] != "syncing+dirty" {
		t.Errorf("state = %q, want syncing+dirty", states["proj_a"])
	}

	close(f.release)
	if r := awaitResult(t, first); r.Err != nil {
		t.Fatalf("first run: %v", r.Err)
	}
	f.awaitStart(t)
	for i, ch := range queued {
		if r := awaitResult(t, ch); r.Err != nil {
			t.Fatalf("queued request %d: %v", i, r.Err)
		}
	}

	if got := len(f.snapshot()); got != 2 {
		t.Errorf("sync ran %d times, want 2 (the first run plus one collapsed follow-up)", got)
	}
	s.Wait()
	if states := s.States(); states["proj_a"] != "idle" {
		t.Errorf("state after both runs = %q, want idle", states["proj_a"])
	}
}

func TestSchedulerRunsNoFollowUpWithoutAChange(t *testing.T) {
	f := newFakeSync()
	close(f.release)
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	if r := awaitResult(t, s.Request("proj_a", SyncOptions{}, nil)); r.Err != nil {
		t.Fatalf("run: %v", r.Err)
	}
	f.awaitStart(t)
	s.Wait()

	if got := len(f.snapshot()); got != 1 {
		t.Errorf("sync ran %d times, want exactly 1", got)
	}
}

func TestSchedulerMergesFollowUpOptions(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	first := s.Request("proj_a", SyncOptions{Offline: true, Dependency: "example.com/a"}, nil)
	f.awaitStart(t)
	s.Request("proj_a", SyncOptions{Offline: true, Force: true, Dependency: "example.com/a"}, nil)
	last := s.Request("proj_a", SyncOptions{Resolve: true, Dependency: "example.com/b"}, nil)

	close(f.release)
	awaitResult(t, first)
	awaitResult(t, last)
	f.awaitStart(t)
	s.Wait()

	calls := f.snapshot()
	if len(calls) != 2 {
		t.Fatalf("sync ran %d times, want 2", len(calls))
	}
	// Priority is set by start for every scheduler-launched run (WATCH-020)
	// — real, non-nil, but not part of what this test is about — so it's
	// checked and cleared separately rather than folded into want below.
	if calls[1].opts.Priority == nil {
		t.Error("follow-up run's SyncOptions.Priority is nil, want a live queue every scheduler-launched run gets")
	}
	got := calls[1].opts
	got.Priority = nil
	want := SyncOptions{Force: true, Resolve: true} // offline: not unanimous; dependency: differed
	if got != want {
		t.Errorf("follow-up options = %+v, want %+v", got, want)
	}
}

func TestSchedulerNeverRunsTwoSyncsAtOnce(t *testing.T) {
	f := newFakeSync()
	close(f.release)
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	var chans []<-chan Result
	for _, id := range []string{"proj_a", "proj_b", "proj_c"} {
		chans = append(chans, s.Request(id, SyncOptions{}, nil))
	}
	for _, ch := range chans {
		if r := awaitResult(t, ch); r.Err != nil {
			t.Fatalf("run: %v", r.Err)
		}
	}
	s.Wait()

	if got := f.maxSeen.Load(); got > 1 {
		t.Errorf("%d syncs ran concurrently, want at most 1 (v1 serializes globally)", got)
	}
}

func TestSchedulerSurvivesFailureAndPanic(t *testing.T) {
	f := newFakeSync()
	close(f.release)
	f.fail = func(projectID string, _ int) error {
		switch projectID {
		case "proj_bad":
			return errors.New("resolve failed")
		case "proj_panic":
			panic("boom")
		}
		return nil
	}
	var logged bytes.Buffer
	var logMu sync.Mutex
	s := NewScheduler(context.Background(), f.run, noGC, func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(&logged, format+"\n", args...)
	})

	if r := awaitResult(t, s.Request("proj_bad", SyncOptions{}, nil)); r.Err == nil {
		t.Error("failed sync reported success")
	}
	if r := awaitResult(t, s.Request("proj_panic", SyncOptions{}, nil)); r.Err == nil || !errorContains(r.Err, "panicked") {
		t.Errorf("panicking sync = %v, want a panic error", r.Err)
	}
	if r := awaitResult(t, s.Request("proj_good", SyncOptions{}, nil)); r.Err != nil {
		t.Errorf("later project = %v, want it to still run", r.Err)
	}

	logMu.Lock()
	defer logMu.Unlock()
	if !errorContains(errors.New(logged.String()), "resolve failed") {
		t.Errorf("log = %q, want the failure reported", logged.String())
	}
}

func TestSchedulerWritesProgressToWaiters(t *testing.T) {
	f := newFakeSync()
	close(f.release)
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	var out bytes.Buffer
	awaitResult(t, s.Request("proj_a", SyncOptions{}, &out))
	if out.String() != "syncing proj_a\n" {
		t.Errorf("progress = %q, want the run's output", out.String())
	}
}

func TestSchedulerShutdownFinishesRunAndFailsQueued(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	running := s.Request("proj_a", SyncOptions{}, nil)
	f.awaitStart(t)
	queued := s.Request("proj_a", SyncOptions{}, nil)

	s.Shutdown()
	if r := awaitResult(t, queued); !errors.Is(r.Err, ErrShuttingDown) {
		t.Errorf("queued request after shutdown = %v, want ErrShuttingDown", r.Err)
	}

	close(f.release)
	if r := awaitResult(t, running); r.Err != nil {
		t.Errorf("in-flight run = %v, want it to finish normally", r.Err)
	}
	s.Wait()

	if r := awaitResult(t, s.Request("proj_b", SyncOptions{}, nil)); !errors.Is(r.Err, ErrShuttingDown) {
		t.Errorf("new request after shutdown = %v, want ErrShuttingDown", r.Err)
	}
	if got := len(f.snapshot()); got != 1 {
		t.Errorf("sync ran %d times after shutdown, want 1 (only the in-flight run)", got)
	}
}

func TestSchedulerGCCollapsesRequestsIntoOneFollowUp(t *testing.T) {
	g := newFakeGC()
	s := NewScheduler(context.Background(), noSync, g.run, nil)

	first := s.RequestGC(false, nil)
	g.awaitStart(t)

	var queued []<-chan GCOutcome
	for range 3 {
		queued = append(queued, s.RequestGC(true, nil))
	}

	close(g.release)
	if r := awaitGCResult(t, first); r.Err != nil {
		t.Fatalf("first run: %v", r.Err)
	}
	g.awaitStart(t)
	for i, ch := range queued {
		if r := awaitGCResult(t, ch); r.Err != nil {
			t.Fatalf("queued request %d: %v", i, r.Err)
		}
	}

	if got := len(g.snapshot()); got != 2 {
		t.Errorf("gc ran %d times, want 2 (the first run plus one collapsed follow-up)", got)
	}
	s.Wait()
}

// TestSchedulerGCDryRunNeverEscalatesWhenCollapsed is the regression test
// for the bug this session caught before it shipped: a caller who
// explicitly asked for a dry run must never have that silently turn into
// a real delete just because their request collapsed with someone else's
// non-dry-run request.
func TestSchedulerGCDryRunNeverEscalatesWhenCollapsed(t *testing.T) {
	g := newFakeGC()
	s := NewScheduler(context.Background(), noSync, g.run, nil)

	first := s.RequestGC(false, nil)
	g.awaitStart(t)
	dryRunWaiter := s.RequestGC(true, nil) // must never run for real

	close(g.release)
	awaitGCResult(t, first)
	awaitGCResult(t, dryRunWaiter)
	s.Wait()

	calls := g.snapshot()
	if len(calls) != 2 {
		t.Fatalf("gc ran %d times, want 2", len(calls))
	}
	if calls[1] != true {
		t.Errorf("collapsed follow-up ran with dryRun=%v, want true — a preview request must never escalate into a real delete", calls[1])
	}
}

// TestSchedulerGCAndSyncExcludeEachOther is the correctness requirement
// docs/scratch/action-controller-proposal.md settles on: GC decides a
// generation is unreferenced by reading state a concurrent sync can be
// actively changing, so the two must never run at once — not a
// simplicity default, a real corruption risk.
func TestSchedulerGCAndSyncExcludeEachOther(t *testing.T) {
	f := newFakeSync()
	g := newFakeGC()
	close(f.release)
	close(g.release)
	s := NewScheduler(context.Background(), f.run, g.run, nil)

	done := make(chan struct{}, 6)
	for range 3 {
		syncCh := s.Request("proj_a", SyncOptions{}, nil)
		gcCh := s.RequestGC(false, nil)
		go func() { <-syncCh; done <- struct{}{} }()
		go func() { <-gcCh; done <- struct{}{} }()
	}
	for range 6 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for sync/gc requests to finish")
		}
	}
	s.Wait()

	maxSync := f.maxSeen.Load()
	maxGC := g.maxSeen.Load()
	if maxSync > 1 || maxGC > 1 {
		t.Errorf("sync saw %d concurrent, gc saw %d concurrent, want at most 1 each (they share the global lock)", maxSync, maxGC)
	}
}

func TestSchedulerGCSurvivesFailureAndPanic(t *testing.T) {
	g := newFakeGC()
	close(g.release)
	g.fail = errors.New("delete failed")
	s := NewScheduler(context.Background(), noSync, g.run, nil)

	if r := awaitGCResult(t, s.RequestGC(false, nil)); r.Err == nil {
		t.Error("failed gc reported success")
	}
	if got := len(g.snapshot()); got != 1 {
		t.Errorf("gc ran %d times, want 1", got)
	}
}

func TestSchedulerGCShutdownFinishesRunAndFailsQueued(t *testing.T) {
	g := newFakeGC()
	s := NewScheduler(context.Background(), noSync, g.run, nil)

	running := s.RequestGC(false, nil)
	g.awaitStart(t)
	queued := s.RequestGC(false, nil)

	s.Shutdown()
	if r := awaitGCResult(t, queued); !errors.Is(r.Err, ErrShuttingDown) {
		t.Errorf("queued gc after shutdown = %v, want ErrShuttingDown", r.Err)
	}

	close(g.release)
	if r := awaitGCResult(t, running); r.Err != nil {
		t.Errorf("in-flight gc = %v, want it to finish normally", r.Err)
	}
	s.Wait()

	if r := awaitGCResult(t, s.RequestGC(false, nil)); !errors.Is(r.Err, ErrShuttingDown) {
		t.Errorf("new gc request after shutdown = %v, want ErrShuttingDown", r.Err)
	}
}

// TestSchedulerLockProjectSerializesSameProject covers scanAndResolve's
// actual use of LockProject: two concurrent holders of the *same*
// project's lock never overlap, but different projects never wait on
// each other.
func TestSchedulerLockProjectSerializesSameProject(t *testing.T) {
	s := NewScheduler(context.Background(), noSync, noGC, nil)

	var mu sync.Mutex
	var concurrent, maxConcurrent int
	hold := func(id string, wg *sync.WaitGroup) {
		defer wg.Done()
		unlock := s.LockProject(id)
		defer unlock()

		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		concurrent--
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go hold("proj_a", &wg)
	}
	wg.Wait()
	if maxConcurrent != 1 {
		t.Errorf("%d holders of proj_a's lock ran concurrently, want exactly 1", maxConcurrent)
	}

	// Different projects don't wait on each other: this must finish well
	// under 5*20ms if proj_a and proj_b's locks are actually independent.
	concurrent, maxConcurrent = 0, 0
	start := time.Now()
	wg.Add(2)
	go hold("proj_a", &wg)
	go hold("proj_b", &wg)
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 60*time.Millisecond {
		t.Errorf("locking two different projects took %s, want ~20ms (they should run concurrently)", elapsed)
	}
}

// TestSchedulerLockProjectEvictsIdleEntries proves scanLocks doesn't grow
// forever: once nothing holds or is waiting for a project's lock, its
// entry is removed, not kept for the rest of the daemon's lifetime.
func TestSchedulerLockProjectEvictsIdleEntries(t *testing.T) {
	s := NewScheduler(context.Background(), noSync, noGC, nil)

	for i := range 50 {
		unlock := s.LockProject(fmt.Sprintf("proj_%d", i))
		unlock()
	}

	s.scanMu.Lock()
	got := len(s.scanLocks)
	s.scanMu.Unlock()
	if got != 0 {
		t.Errorf("scanLocks has %d entries after every lock was released, want 0", got)
	}

	// A lock still held (or still wanted) must not be evicted out from
	// under its holder.
	unlock := s.LockProject("proj_held")
	s.scanMu.Lock()
	got = len(s.scanLocks)
	s.scanMu.Unlock()
	if got != 1 {
		t.Errorf("scanLocks has %d entries while proj_held is locked, want 1", got)
	}
	unlock()
}

// TestSchedulerActionTimesOutWithoutWedgingQueue is the direct test for
// maxActionDuration: a run that respects ctx cancellation (as every real
// SyncFunc/GCFunc does — the qdrant/ollama clients and git subprocess
// calls are all ctx-bound) gets cut off at the deadline, releases the
// global lock, and lets the next queued action proceed — instead of one
// hung run wedging the queue shut forever.
func TestSchedulerActionTimesOutWithoutWedgingQueue(t *testing.T) {
	original := maxActionDuration
	maxActionDuration = 20 * time.Millisecond
	defer func() { maxActionDuration = original }()

	hangs := func(ctx context.Context, _ string, _ SyncOptions, _ io.Writer) (api.SyncResult, error) {
		<-ctx.Done()
		return api.SyncResult{}, ctx.Err()
	}
	f := newFakeSync()
	close(f.release)
	run := func(ctx context.Context, projectID string, opts SyncOptions, out io.Writer) (api.SyncResult, error) {
		if projectID == "proj_stuck" {
			return hangs(ctx, projectID, opts, out)
		}
		return f.run(ctx, projectID, opts, out)
	}
	s := NewScheduler(context.Background(), run, noGC, nil)

	stuck := s.Request("proj_stuck", SyncOptions{}, nil)
	if r := awaitResult(t, stuck); r.Err == nil {
		t.Error("hung run reported success, want a context-deadline error")
	}

	// The lock the hung run held must already be free: a request queued
	// right behind it runs promptly, not after 30 real minutes.
	next := s.Request("proj_next", SyncOptions{}, nil)
	if r := awaitResult(t, next); r.Err != nil {
		t.Errorf("run after the timed-out one = %v, want it to proceed normally", r.Err)
	}
	s.Wait()
}

// TestBumpPriorityReturnsFalseWithNoSyncRunning is WATCH-020's simplest
// case: nothing to prioritize when the project isn't syncing at all.
func TestBumpPriorityReturnsFalseWithNoSyncRunning(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	if s.BumpPriority("proj_idle", "example.com/a") {
		t.Error("BumpPriority = true with no sync running, want false")
	}
}

// TestBumpPriorityReachesLiveRunningSync is WATCH-020's core plumbing
// proof: a bump issued while a sync is genuinely in flight reaches the
// exact SyncPriority object that run's own SyncOptions.Priority carries
// — the same live object RunSync's action loop drains between actions
// — not a copy, and not silently dropped.
func TestBumpPriorityReachesLiveRunningSync(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	result := s.Request("proj_a", SyncOptions{}, nil)
	f.awaitStart(t)

	if !s.BumpPriority("proj_a", "example.com/urgent") {
		t.Fatal("BumpPriority = false while a sync is running, want true")
	}

	calls := f.snapshot()
	if len(calls) != 1 || calls[0].opts.Priority == nil {
		t.Fatalf("run's own SyncOptions.Priority is nil, want the live queue BumpPriority just wrote to")
	}
	pending := calls[0].opts.Priority.Drain()
	if len(pending) != 1 || pending[0] != "example.com/urgent" {
		t.Errorf("Priority.Drain() = %v, want [example.com/urgent]", pending)
	}

	close(f.release)
	awaitResult(t, result)
}

// TestBumpPriorityFalseAfterSyncFinishes proves the priority queue is
// scoped to one run's lifetime — a bump after the run that would have
// consumed it already finished correctly reports false, rather than
// silently attaching to whatever happens to run next.
func TestBumpPriorityFalseAfterSyncFinishes(t *testing.T) {
	f := newFakeSync()
	close(f.release)
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	awaitResult(t, s.Request("proj_a", SyncOptions{}, nil))
	s.Wait()

	if s.BumpPriority("proj_a", "example.com/a") {
		t.Error("BumpPriority = true after the run finished, want false")
	}
}

// noSync is a SyncFunc for tests that only exercise GC — never expected
// to be called.
func noSync(context.Context, string, SyncOptions, io.Writer) (api.SyncResult, error) {
	panic("sync should not run in this test")
}

func errorContains(err error, want string) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte(want))
}

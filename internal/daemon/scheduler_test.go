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

func TestSchedulerCollapsesRequestsIntoOneFollowUp(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, nil)

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
	s := NewScheduler(context.Background(), f.run, nil)

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
	s := NewScheduler(context.Background(), f.run, nil)

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
	want := SyncOptions{Force: true, Resolve: true} // offline: not unanimous; dependency: differed
	if calls[1].opts != want {
		t.Errorf("follow-up options = %+v, want %+v", calls[1].opts, want)
	}
}

func TestSchedulerNeverRunsTwoSyncsAtOnce(t *testing.T) {
	f := newFakeSync()
	close(f.release)
	s := NewScheduler(context.Background(), f.run, nil)

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
	s := NewScheduler(context.Background(), f.run, func(format string, args ...any) {
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
	s := NewScheduler(context.Background(), f.run, nil)

	var out bytes.Buffer
	awaitResult(t, s.Request("proj_a", SyncOptions{}, &out))
	if out.String() != "syncing proj_a\n" {
		t.Errorf("progress = %q, want the run's output", out.String())
	}
}

func TestSchedulerShutdownFinishesRunAndFailsQueued(t *testing.T) {
	f := newFakeSync()
	s := NewScheduler(context.Background(), f.run, nil)

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

func errorContains(err error, want string) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte(want))
}

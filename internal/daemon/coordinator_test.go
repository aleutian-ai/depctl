package daemon

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

func testDep(name, version string) domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: name},
		Version:    version,
	}
}

func TestBuildKeyDistinguishesDependencies(t *testing.T) {
	a := buildKey(testDep("example.com/a", "v1.0.0"))
	b := buildKey(testDep("example.com/b", "v1.0.0"))
	c := buildKey(testDep("example.com/a", "v2.0.0"))
	if a == b || a == c || b == c {
		t.Errorf("buildKey collided: a=%q b=%q c=%q", a, b, c)
	}
}

// TestBuildCoalescesConcurrentCallersForSameKey proves COORD-001's
// central guarantee: many genuinely concurrent callers for the identical
// dependency+version produce exactly one real build, not one per caller.
//
// singleflight has no hook for "a concurrent caller has registered as a
// joiner" — proving true overlap without one means either a short sleep
// or, as here, racing enough concurrent callers at once that a brief
// sleep gives overwhelming (not just probable) confidence they've all
// reached group.Do before fn is allowed to return. If coalescing were
// broken, this test would flake toward failure, never toward a false
// pass — calls > 1 is still calls > 1 no matter how many callers raced.
func TestBuildCoalescesConcurrentCallersForSameKey(t *testing.T) {
	c := NewBuildCoordinator()
	dep := testDep("example.com/widget", "v1.0.0")

	var calls int32
	block := make(chan struct{})
	fn := func() error {
		atomic.AddInt32(&calls, 1)
		<-block // hold the one real build open so every racer can join it
		return nil
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = c.Build(dep, fn)
		}(i)
	}

	close(start)
	time.Sleep(20 * time.Millisecond) // let all n racers reach group.Do
	close(block)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fn called %d times across %d concurrent callers, want exactly 1", got, n)
	}
}

// TestBuildAllowsFreshAttemptAfterEarlierOneFinishes proves singleflight
// coalescing doesn't wrongly cache across non-overlapping calls — two
// sequential Builds for the same key must run fn twice, not once.
func TestBuildAllowsFreshAttemptAfterEarlierOneFinishes(t *testing.T) {
	c := NewBuildCoordinator()
	dep := testDep("example.com/widget", "v1.0.0")

	var calls int32
	fn := func() error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	if err := c.Build(dep, fn); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if err := c.Build(dep, fn); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("fn called %d times across two sequential Builds, want 2", got)
	}
}

// TestBuildPropagatesErrorAndAllowsRetry proves a failed build reports
// its real error to the caller, and doesn't permanently poison the key —
// a later attempt runs fresh rather than replaying the stale failure.
func TestBuildPropagatesErrorAndAllowsRetry(t *testing.T) {
	c := NewBuildCoordinator()
	dep := testDep("example.com/widget", "v1.0.0")

	wantErr := errors.New("acquisition failed")
	if err := c.Build(dep, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Errorf("Build error = %v, want %v", err, wantErr)
	}

	ran := false
	if err := c.Build(dep, func() error { ran = true; return nil }); err != nil {
		t.Fatalf("Build after earlier failure: %v", err)
	}
	if !ran {
		t.Error("fn did not run on the retry after the earlier failure")
	}
}

// TestProtectFromGCExcludesGC proves ProtectFromGC gives a plain state
// mutation (addReference/DropReference-shaped — no build identity to
// coalesce) the same GC-exclusion Build gives an actual generation
// build: GC must wait for it to finish before running.
func TestProtectFromGCExcludesGC(t *testing.T) {
	c := NewBuildCoordinator()

	release := make(chan struct{})
	started := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- c.ProtectFromGC(func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	gcAcquired := make(chan func())
	go func() { gcAcquired <- c.ExcludeForGC() }()

	select {
	case <-gcAcquired:
		t.Fatal("ExcludeForGC returned while a ProtectFromGC call was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatalf("ProtectFromGC: %v", err)
	}

	select {
	case release := <-gcAcquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("ExcludeForGC never acquired the gate after the mutation finished")
	}
}

// TestBuildTimingDistinguishesGateWaitFromRealWork proves the
// instrumentation COORD-002's own ticket calls for: a Build call that's
// genuinely blocked behind GC reports that as GateWait, not lumped into
// Work — exactly the distinction needed to tell "was blocked by
// coordination" apart from "did real, unavoidable work."
func TestBuildTimingDistinguishesGateWaitFromRealWork(t *testing.T) {
	c := NewBuildCoordinator()

	var mu sync.Mutex
	var timings []BuildTiming
	c.SetTimingHook(func(key string, t BuildTiming) {
		mu.Lock()
		defer mu.Unlock()
		timings = append(timings, t)
	})

	// Hold the gate exclusively (as GC would) before the Build call
	// below ever starts, for a known, deliberately generous duration —
	// long enough that a real GateWait measurement is unmistakable next
	// to typical scheduling noise, short enough not to slow the suite.
	const holdFor = 150 * time.Millisecond
	release := c.ExcludeForGC()
	time.AfterFunc(holdFor, func() { release() })

	start := time.Now()
	if err := c.Build(testDep("example.com/widget", "v1.0.0"), func() error {
		return nil
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	total := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	if len(timings) != 1 {
		t.Fatalf("onTiming called %d times, want 1", len(timings))
	}
	got := timings[0]
	if got.GateWait < holdFor/2 {
		t.Errorf("GateWait = %v, want at least ~%v (the gate was held that long before Build could start)", got.GateWait, holdFor)
	}
	if got.GateWait > total {
		t.Errorf("GateWait = %v, want <= total elapsed %v", got.GateWait, total)
	}
	if got.Work > 50*time.Millisecond {
		t.Errorf("Work = %v, want it to reflect only fn's own near-instant run, not the gate wait", got.Work)
	}
}

// TestExcludeForGCWaitsForInFlightBuildsAndBlocksNewOnes proves both
// halves of the RWMutex contract this coordinator relies on: GC waits
// for every currently in-flight build (not just one), and — the writer-
// priority property — a brand new Build call arriving while GC is
// waiting queues behind it instead of overtaking it.
func TestExcludeForGCWaitsForInFlightBuildsAndBlocksNewOnes(t *testing.T) {
	c := NewBuildCoordinator()
	depA := testDep("example.com/a", "v1.0.0")
	depB := testDep("example.com/b", "v1.0.0")

	release := make(chan struct{})
	started := make(chan struct{})
	buildDone := make(chan error, 1)
	go func() {
		buildDone <- c.Build(depA, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started // depA's build genuinely holds RLock now

	gcAcquired := make(chan func())
	go func() { gcAcquired <- c.ExcludeForGC() }()

	select {
	case <-gcAcquired:
		t.Fatal("ExcludeForGC returned while depA's build was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	newBuildStarted := make(chan struct{})
	newBuildDone := make(chan error, 1)
	go func() {
		newBuildDone <- c.Build(depB, func() error {
			close(newBuildStarted)
			return nil
		})
	}()

	select {
	case <-newBuildStarted:
		t.Fatal("a new Build call for an unrelated dependency proceeded while GC was waiting")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-buildDone; err != nil {
		t.Fatalf("depA Build: %v", err)
	}

	var gcRelease func()
	select {
	case gcRelease = <-gcAcquired:
	case <-time.After(time.Second):
		t.Fatal("ExcludeForGC never acquired the gate after depA's build finished")
	}

	select {
	case <-newBuildStarted:
		t.Fatal("depB's Build proceeded while GC still holds the gate")
	default:
	}

	gcRelease()

	select {
	case <-newBuildStarted:
	case <-time.After(time.Second):
		t.Fatal("depB's Build never proceeded after GC released the gate")
	}
	if err := <-newBuildDone; err != nil {
		t.Fatalf("depB Build: %v", err)
	}
}

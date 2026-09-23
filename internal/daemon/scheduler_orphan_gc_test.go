package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSchedulerOrphanGCAndSyncExcludeEachOther proves RequestOrphanGC
// shares RequestGC's own coordinator.ExcludeForGC exclusion — GC-002's
// orphan path must never interleave with an in-flight build any more
// than the reference-based path does.
func TestSchedulerOrphanGCAndSyncExcludeEachOther(t *testing.T) {
	f := newFakeSync()
	g := newFakeGC()
	close(f.release)
	close(g.release)
	s := NewScheduler(context.Background(), f.run, noGC, nil)

	done := make(chan struct{}, 6)
	for range 3 {
		syncCh := s.Request("proj_a", SyncOptions{}, nil)
		go func() { <-syncCh; done <- struct{}{} }()
		go func() {
			s.RequestOrphanGC(context.Background(), g.run, false, nil)
			done <- struct{}{}
		}()
	}
	for range 6 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for sync/orphan-gc requests to finish")
		}
	}
	s.Wait()

	if maxSync, maxGC := f.maxSeen.Load(), g.maxSeen.Load(); maxSync > 1 || maxGC > 1 {
		t.Errorf("sync saw %d concurrent, orphan-gc saw %d concurrent, want at most 1 each (coordinator.ExcludeForGC must exclude every in-flight Build)", maxSync, maxGC)
	}
}

// TestSchedulerOrphanGCDoesNotCoalesceRequests is the key behavioral
// difference from RequestGC: two concurrent orphan-GC requests each get
// their own real run — orphan GC is deliberately manual/opt-in, so
// there's no automatic-firing scenario to collapse duplicate requests
// from in the first place (see RequestOrphanGC's own doc comment).
func TestSchedulerOrphanGCDoesNotCoalesceRequests(t *testing.T) {
	g := newFakeGC()
	s := NewScheduler(context.Background(), noSync, noGC, nil)

	results := make(chan struct{}, 2)
	go func() {
		s.RequestOrphanGC(context.Background(), g.run, false, nil)
		results <- struct{}{}
	}()
	g.awaitStart(t)
	go func() {
		s.RequestOrphanGC(context.Background(), g.run, false, nil)
		results <- struct{}{}
	}()
	close(g.release)

	for range 2 {
		select {
		case <-results:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for both orphan-gc requests to finish")
		}
	}
	s.Wait()

	if got := len(g.snapshot()); got != 2 {
		t.Errorf("orphan-gc ran %d times, want 2 (each request gets its own real run, no coalescing)", got)
	}
}

// TestSchedulerOrphanGCRejectsAfterShutdown mirrors RequestGC's own
// shutdown behavior — a new request after Shutdown gets ErrShuttingDown
// immediately, never silently hangs or runs anyway.
func TestSchedulerOrphanGCRejectsAfterShutdown(t *testing.T) {
	g := newFakeGC()
	close(g.release)
	s := NewScheduler(context.Background(), noSync, noGC, nil)
	s.Shutdown()

	_, err := s.RequestOrphanGC(context.Background(), g.run, false, nil)
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("RequestOrphanGC after Shutdown = %v, want ErrShuttingDown", err)
	}
	if got := len(g.snapshot()); got != 0 {
		t.Errorf("orphan-gc ran %d times after shutdown, want 0", got)
	}
}

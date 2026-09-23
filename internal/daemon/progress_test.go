package daemon

import (
	"context"
	"io"
	"sync"
	"testing"

	"aleutian-ai/ragctl/internal/daemon/api"
)

func TestSyncProgressTracksInFlightAndDone(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(4)
	p.Begin("zeta")
	p.Begin("alpha")
	p.SetChunks("alpha", 30, 120)
	p.SetChunks("unknown", 1, 1) // never began: ignored, no panic

	snap := p.Snapshot()
	if snap.Total != 4 || snap.Done != 0 || len(snap.InFlight) != 2 {
		t.Fatalf("snapshot = %+v, want total 4, done 0, two in flight", snap)
	}
	if snap.InFlight[0].Name != "alpha" || snap.InFlight[0].ChunksDone != 30 || snap.InFlight[0].ChunksTotal != 120 {
		t.Errorf("in flight = %+v, want alpha 30/120 first (sorted by name)", snap.InFlight)
	}

	p.Finish("alpha", false)
	p.Finish("zeta", true)
	p.Finish("reference-only", false) // an action that never began still counts
	snap = p.Snapshot()
	if snap.Done != 3 || snap.Failed != 1 || len(snap.InFlight) != 0 {
		t.Errorf("after finishing = %+v, want done 3, failed 1, nothing in flight", snap)
	}
}

func TestSyncProgressNilReceiverIsSafe(t *testing.T) {
	var p *SyncProgress
	p.SetTotal(1)
	p.Begin("a")
	p.SetChunks("a", 1, 2)
	p.Finish("a", true)
	if snap := p.Snapshot(); snap.Total != 0 || snap.Done != 0 {
		t.Errorf("nil snapshot = %+v, want zero", snap)
	}
}

func TestSyncProgressIsSafeUnderConcurrentWorkers(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(200)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				name := string(rune('a'+w)) + string(rune('a'+i))
				p.Begin(name)
				p.SetChunks(name, i, 25)
				p.Snapshot()
				p.Finish(name, false)
			}
		}()
	}
	wg.Wait()
	if snap := p.Snapshot(); snap.Done != 200 || len(snap.InFlight) != 0 {
		t.Errorf("snapshot = %+v, want 200 done, none in flight", snap)
	}
}

// TestSchedulerSyncProgressLiveThenLastRun: while a run is in flight the
// query shows its live counters; once it ends, the last run's numbers
// stay but syncing goes false — no stale "still syncing". A project that
// never synced reports zeros, not an error.
func TestSchedulerSyncProgressLiveThenLastRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	run := func(ctx context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, out io.Writer) (api.SyncResult, error) {
		opts.Progress.SetTotal(3)
		opts.Progress.Begin("a")
		opts.Progress.SetChunks("a", 5, 10)
		close(started)
		<-release
		opts.Progress.Finish("a", false)
		return api.SyncResult{ProjectID: projectID}, nil
	}
	s := NewScheduler(context.Background(), run, noGC, nil)

	if got := s.SyncProgress("proj_never"); got.Syncing || got.Total != 0 || got.Done != 0 {
		t.Errorf("never-synced project = %+v, want all zero", got)
	}

	waiter := s.Request("proj_a", SyncOptions{}, nil)
	<-started
	live := s.SyncProgress("proj_a")
	if !live.Syncing || live.Total != 3 || len(live.InFlight) != 1 || live.InFlight[0].ChunksDone != 5 {
		t.Errorf("mid-run = %+v, want syncing, total 3, a at 5/10 chunks", live)
	}

	close(release)
	awaitResult(t, waiter)
	s.Wait()
	last := s.SyncProgress("proj_a")
	if last.Syncing || last.Done != 1 || last.Total != 3 || len(last.InFlight) != 0 {
		t.Errorf("after run = %+v, want not syncing, done 1 of 3, nothing in flight", last)
	}
}

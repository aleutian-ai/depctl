package daemon

import (
	"context"
	"io"
	"sync"
	"testing"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// TestSyncProgressSurvivesAFastNoOpFollowUp is MCP-004's own regression:
// live-reproduced (a real offline sync of two Node dependencies, both
// failing with "embedding backend unreachable") that a genuinely new
// run starting and finishing — even a no-op with nothing to plan — must
// never silently discard the previous, real run's failure. Two runs in
// strict sequence: run A fails 2 real actions; run B (started only
// after A has fully finished, per s.Wait()) finds nothing to do.
// SyncProgress must still report run A's real outcome afterward.
func TestSyncProgressSurvivesAFastNoOpFollowUp(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			// A real, meaningful run: planned 2 actions, both failed —
			// exactly the live-found "embedding backend unreachable" shape.
			opts.Progress.SetTotal(2)
			opts.Progress.Finish("chalk", true)
			opts.Progress.Finish("commander", true)
			return api.SyncResult{ProjectID: projectID, Failed: 2}, nil
		}
		// A genuine no-op: nothing new to plan (everything already
		// attempted, no --force, no new dependency).
		opts.Progress.SetTotal(0)
		return api.SyncResult{ProjectID: projectID}, nil
	}

	s := NewScheduler(context.Background(), run, noGC, nil)
	defer s.Shutdown()

	if r := awaitResult(t, s.Request("proj_offline", SyncOptions{}, nil)); r.Err != nil {
		t.Fatalf("run A: %v", r.Err)
	}
	s.Wait() // run A's finish() (and lastMeaningful capture) has completed

	got := s.SyncProgress("proj_offline")
	if got.Total != 2 || got.Failed != 2 || got.Done != 2 {
		t.Fatalf("SyncProgress after run A = %+v, want {done:2 failed:2 total:2}", got)
	}

	if r := awaitResult(t, s.Request("proj_offline", SyncOptions{}, nil)); r.Err != nil {
		t.Fatalf("run B: %v", r.Err)
	}
	s.Wait() // run B — a genuine no-op — has also fully finished

	got = s.SyncProgress("proj_offline")
	if got.Total != 2 || got.Failed != 2 || got.Done != 2 {
		t.Errorf("SyncProgress after a no-op follow-up = %+v, want run A's real outcome still intact ({done:2 failed:2 total:2}), not empty counters", got)
	}
}

// TestSyncProgressSurvivesADirtyCoalescedFollowUp is the actual,
// precise regression: the first version of this fix only captured
// lastMeaningful on the "truly idle" path of finish() — the
// dirty-coalescing branch (a request arrives *while* a run is still
// executing) returns early, immediately starting the queued follow-up
// without ever reaching that capture code. Live-found: this is exactly
// the sequence a real ambient sync (ScanProject's automatic trigger)
// followed immediately by an explicit sync_project call produces — the
// ambient run's real failure was discarded the moment the coalesced
// follow-up (finding nothing new to do) finished. Deterministic, not
// timing-dependent: a gated fake run lets the test control exactly when
// the second request arrives relative to the first run's execution.
func TestSyncProgressSurvivesADirtyCoalescedFollowUp(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		started <- struct{}{}
		if n == 1 {
			<-release // held open until the test has queued a dirty follow-up
			opts.Progress.SetTotal(2)
			opts.Progress.Finish("chalk", true)
			opts.Progress.Finish("commander", true)
			return api.SyncResult{ProjectID: projectID, Failed: 2}, nil
		}
		// The coalesced follow-up: nothing new to plan.
		opts.Progress.SetTotal(0)
		return api.SyncResult{ProjectID: projectID}, nil
	}

	s := NewScheduler(context.Background(), run, noGC, nil)
	defer s.Shutdown()

	first := s.Request("proj_offline", SyncOptions{}, nil)
	<-started // run 1 (the real, failing ambient sync) has started

	// The follow-up (an explicit sync_project call) arrives while run 1
	// is still executing — this is the dirty-coalescing path, not a
	// fresh idle start.
	second := s.Request("proj_offline", SyncOptions{}, nil)
	if states := s.States(); states["proj_offline"] != "syncing+dirty" {
		t.Fatalf("state = %q, want syncing+dirty (the follow-up must coalesce, not start fresh)", states["proj_offline"])
	}

	close(release) // let run 1 finish; finish() immediately starts run 2 (dirty)
	if r := awaitResult(t, first); r.Err != nil {
		t.Fatalf("run 1: %v", r.Err)
	}
	if r := awaitResult(t, second); r.Err != nil {
		t.Fatalf("run 2: %v", r.Err)
	}
	s.Wait()

	got := s.SyncProgress("proj_offline")
	if got.Total != 2 || got.Failed != 2 || got.Done != 2 {
		t.Errorf("SyncProgress after a dirty-coalesced no-op follow-up = %+v, want run 1's real outcome preserved ({done:2 failed:2 total:2})", got)
	}
}

// TestSyncProgressRanDistinguishesNeverSyncedFromNothingToDo is the
// companion regression to this file's other tests, live-found the same
// way: an agent calling sync_progress for a project whose sync just
// found nothing new to build (every dependency already had a current
// generation elsewhere in the fleet) saw the exact same zero-valued
// SyncProgress a project that had *never* synced would — indistinguishable,
// and actively misleading (a real MCP session concluded the sync tool
// must be disabled). Ran must be true the moment a real run finishes,
// even a Total==0 one that lastMeaningful itself deliberately ignores.
func TestSyncProgressRanDistinguishesNeverSyncedFromNothingToDo(t *testing.T) {
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		// Every dependency already has a current generation elsewhere —
		// a genuine, correct no-op.
		opts.Progress.SetTotal(0)
		return api.SyncResult{ProjectID: projectID}, nil
	}

	s := NewScheduler(context.Background(), run, noGC, nil)
	defer s.Shutdown()

	before := s.SyncProgress("proj_never_synced")
	if before.Ran {
		t.Errorf("SyncProgress for an unregistered/never-synced project = %+v, want Ran false", before)
	}

	if r := awaitResult(t, s.Request("proj_never_synced", SyncOptions{}, nil)); r.Err != nil {
		t.Fatalf("run: %v", r.Err)
	}
	s.Wait()

	after := s.SyncProgress("proj_never_synced")
	if !after.Ran {
		t.Errorf("SyncProgress after a real no-op run = %+v, want Ran true (a sync did run, it just found nothing to do)", after)
	}
	if after.Total != 0 || after.Done != 0 || after.Failed != 0 {
		t.Errorf("SyncProgress after a no-op run = %+v, want all-zero counters alongside Ran true", after)
	}
}

// TestSyncProgressUpdatesOnANewMeaningfulRun proves the fix doesn't
// overcorrect into permanent staleness: a later run that actually finds
// and attempts real new work must still replace lastMeaningful with its
// own outcome.
func TestSyncProgressUpdatesOnANewMeaningfulRun(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			opts.Progress.SetTotal(2)
			opts.Progress.Finish("chalk", true)
			opts.Progress.Finish("commander", true)
			return api.SyncResult{ProjectID: projectID, Failed: 2}, nil
		}
		// A real retry that succeeds this time (e.g. --force after the
		// embedding backend came back).
		opts.Progress.SetTotal(2)
		opts.Progress.Finish("chalk", false)
		opts.Progress.Finish("commander", false)
		return api.SyncResult{ProjectID: projectID, Synced: 2}, nil
	}

	s := NewScheduler(context.Background(), run, noGC, nil)
	defer s.Shutdown()

	awaitResult(t, s.Request("proj_retry", SyncOptions{}, nil))
	s.Wait()
	awaitResult(t, s.Request("proj_retry", SyncOptions{Force: true}, nil))
	s.Wait()

	got := s.SyncProgress("proj_retry")
	if got.Failed != 0 || got.Done != 2 || got.Total != 2 {
		t.Errorf("SyncProgress after a real successful retry = %+v, want {done:2 failed:0 total:2}, not the stale failure", got)
	}
}

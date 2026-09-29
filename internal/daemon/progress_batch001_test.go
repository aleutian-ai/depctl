package daemon

import (
	"testing"
	"time"
)

// syncOneAction is a small test helper: Begin, sleep for dur (a stand-in
// for real elapsed build time), then Finish — used throughout this file
// to produce controllable, distinguishable samples for
// Observed/Estimate's median/p90/confidence computation.
func syncOneAction(p *SyncProgress, name string, dur time.Duration, failed bool) {
	p.Begin(name)
	time.Sleep(dur)
	p.Finish(name, failed)
}

// TestSyncProgressMedianResistsAnOutlierThatWouldSkewAMean is BATCH-001
// Option D's core regression: STRESS-005/006's real timing data is
// heavily skewed (p90 roughly 4x the median, a handful of large
// dependencies dominating total runtime), so a naive average would be
// pulled far above what most dependencies actually take. Nine fast
// completions plus one much slower one must leave the median close to
// the fast cluster, with p90 reflecting the outlier separately — not a
// single blended average number.
func TestSyncProgressMedianResistsAnOutlierThatWouldSkewAMean(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(10)
	p.SetPlanned([]string{"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "slow"})

	const fast = 10 * time.Millisecond
	const slow = 150 * time.Millisecond
	for i := 0; i < 9; i++ {
		syncOneAction(p, string(rune('a'+i))+"0", fast, false)
	}
	syncOneAction(p, "slow", slow, false)

	snap := p.Snapshot()
	if snap.Observed == nil {
		t.Fatal("Observed is nil, want data after 10 completions")
	}
	if snap.Observed.Samples != 10 {
		t.Errorf("Samples = %d, want 10", snap.Observed.Samples)
	}

	naiveMean := (9*fast + slow).Seconds() / 10
	median := snap.Observed.MedianDependencySeconds
	if median >= naiveMean {
		t.Errorf("median %.4fs is not below the naive mean %.4fs — the outlier is skewing it the way a plain average would", median, naiveMean)
	}
	// The median should sit close to the fast cluster (well under half
	// the slow duration), and p90 should clearly reflect the outlier —
	// the two numbers together tell a caller what an average alone
	// would hide.
	if median > slow.Seconds()/2 {
		t.Errorf("median %.4fs is too close to the slow outlier %.4fs — want it anchored to the fast cluster", median, slow.Seconds())
	}
	if snap.Observed.P90DependencySeconds < median*2 {
		t.Errorf("p90 %.4fs is not meaningfully above median %.4fs — should reflect the slow outlier", snap.Observed.P90DependencySeconds, median)
	}
}

// TestSyncProgressEstimateConfidenceLowBelowSampleFloor proves the
// design refinement made explicit in BATCH-001: never report a
// confident-looking ETA from just the first few completions.
func TestSyncProgressEstimateConfidenceLowBelowSampleFloor(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(3)
	p.SetPlanned([]string{"a", "b", "c"})
	syncOneAction(p, "a", time.Millisecond, false)
	syncOneAction(p, "b", time.Millisecond, false)

	snap := p.Snapshot()
	if snap.Observed == nil || snap.Observed.Samples != 2 {
		t.Fatalf("Observed = %+v, want 2 samples reported even though confidence is low", snap.Observed)
	}
	if snap.Estimate == nil {
		t.Fatal("Estimate is nil — an estimate should still be reported, just low-confidence, not withheld")
	}
	if snap.Estimate.Confidence != "low" {
		t.Errorf("Confidence = %q, want \"low\" with only 2 samples", snap.Estimate.Confidence)
	}
}

// TestSyncProgressEstimateConfidenceRisesWithMoreTightSamples confirms
// the other end: enough samples with low observed skew earns "high".
func TestSyncProgressEstimateConfidenceRisesWithMoreTightSamples(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(30)
	names := make([]string, 30)
	for i := range names {
		names[i] = string(rune('a' + i%26))
	}
	p.SetPlanned(names)
	for i, name := range names {
		// Tight spread: 5-6ms, never more than 20% skew.
		dur := 5 * time.Millisecond
		if i%2 == 0 {
			dur = 6 * time.Millisecond
		}
		syncOneAction(p, name, dur, false)
	}

	snap := p.Snapshot()
	if snap.Estimate == nil {
		t.Fatal("Estimate is nil")
	}
	if snap.Estimate.Confidence != "high" {
		t.Errorf("Confidence = %q, want \"high\" with 30 tightly-clustered samples", snap.Estimate.Confidence)
	}
}

// TestSyncProgressPendingDropsOnFinishRegardlessOfOutcome proves Pending
// reflects real remaining scope: it starts as everything SetPlanned
// named, and a name drops out the moment its action finishes, whether
// it succeeded or failed — a failed dependency isn't still "pending."
func TestSyncProgressPendingDropsOnFinishRegardlessOfOutcome(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(3)
	p.SetPlanned([]string{"a", "b", "c"})

	snap := p.Snapshot()
	if len(snap.Pending) != 3 {
		t.Fatalf("Pending before any completion = %v, want all 3 planned names", snap.Pending)
	}

	p.Begin("a")
	p.Finish("a", false) // succeeded
	p.Begin("b")
	p.Finish("b", true) // failed

	snap = p.Snapshot()
	if len(snap.Pending) != 1 || snap.Pending[0] != "c" {
		t.Errorf("Pending after a succeeded and b failed = %v, want only [c] — both finished actions must drop regardless of outcome", snap.Pending)
	}
}

// TestSyncProgressSnapshotReportsNoEstimateForANoOpRun proves a genuine
// no-op (nothing planned, nothing ever begun) reports no Observed/
// Estimate data at all, rather than a divide-by-zero or a misleading
// zero-valued estimate.
func TestSyncProgressSnapshotReportsNoEstimateForANoOpRun(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(0)
	p.SetPlanned(nil)

	snap := p.Snapshot()
	if snap.Observed != nil {
		t.Errorf("Observed = %+v, want nil for a no-op run", snap.Observed)
	}
	if snap.Estimate != nil {
		t.Errorf("Estimate = %+v, want nil for a no-op run", snap.Estimate)
	}
	if len(snap.Pending) != 0 {
		t.Errorf("Pending = %v, want empty for a no-op run", snap.Pending)
	}
}

// TestSyncProgressFinishWithoutBeginRecordsNoDuration mirrors
// TestSyncProgressTracksInFlightAndDone's existing "reference-only"
// case (an action kind that never calls Begin, per MCP-004's own
// Begin/Finish scoping to SYNC_VERSION only) at the timing-sample
// level: it must count toward Done, but never toward Observed's sample
// count, since there's no real elapsed time to report.
func TestSyncProgressFinishWithoutBeginRecordsNoDuration(t *testing.T) {
	p := &SyncProgress{}
	p.SetTotal(1)
	p.Finish("add-reference-only", false)

	snap := p.Snapshot()
	if snap.Done != 1 {
		t.Errorf("Done = %d, want 1", snap.Done)
	}
	if snap.Observed != nil {
		t.Errorf("Observed = %+v, want nil — no Begin call means no real duration sample", snap.Observed)
	}
}

package cli

import (
	"context"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/daemon"
)

// TestRunSyncExhaustedBudgetStopsCleanlyInsteadOfCascading is BATCH-001
// Option C's regression: STRESS-005 found that once the shared batch
// context (internal/daemon/scheduler.go's maxActionDuration) runs out
// partway through an untargeted sync, every remaining action was
// independently popped and independently failed with the same generic
// "context deadline exceeded" — 407 of 561 dependencies in one real run,
// reading as hundreds of unrelated per-dependency failures instead of
// one exhausted shared clock. A context that's already Done() before
// RunSync's worker loop even starts must produce exactly one clear
// aggregate message and attempt zero actions, not cascade.
func TestRunSyncExhaustedBudgetStopsCleanlyInsteadOfCascading(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	badgerStore, err := openDataStore()
	if err != nil {
		t.Fatalf("openDataStore: %v", err)
	}
	defer store.Close()
	defer badgerStore.Close()

	cfg, err := loadRagctlConfig()
	if err != nil {
		t.Fatalf("loadRagctlConfig: %v", err)
	}

	// Already-cancelled — simulates the shared batch context having run
	// out before this worker even started, the exact shape found live.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out strings.Builder
	coordinator := daemon.NewBuildCoordinator()
	synced, failed, skipped, err := RunSync(ctx, coordinator, store, badgerStore, cfg, "", nil, false, false, &out, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if synced != 0 || failed != 0 || skipped != 0 {
		t.Errorf("RunSync = synced=%d failed=%d skipped=%d, want all 0 — an already-exhausted budget must attempt nothing", synced, failed, skipped)
	}

	text := out.String()
	if strings.Count(text, "budget exceeded") != 1 {
		t.Errorf("output contains %d \"budget exceeded\" messages, want exactly 1 (one aggregate message, not one per worker):\n%s", strings.Count(text, "budget exceeded"), text)
	}
	if strings.Contains(text, "FAIL") {
		t.Errorf("output contains a per-action FAIL line — the cascade this fix exists to prevent:\n%s", text)
	}
	if strings.Contains(text, "context deadline exceeded") {
		t.Errorf("output leaked the raw \"context deadline exceeded\" error — must be the one clear aggregate message instead:\n%s", text)
	}
	if !strings.Contains(text, "after 0 of") {
		t.Errorf("output missing \"after 0 of <total>\" — nothing had a chance to complete before the budget was already gone:\n%s", text)
	}
	if !strings.Contains(text, "re-run to continue") {
		t.Errorf("output missing actionable re-run guidance:\n%s", text)
	}
}

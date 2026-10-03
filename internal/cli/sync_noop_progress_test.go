package cli

import (
	"context"
	"io"
	"testing"

	"aleutian-ai/ragctl/internal/daemon"
	"aleutian-ai/ragctl/internal/domain"
)

// TestRunSyncNoopActionsDoNotCountAsProgress is MCP-004's own deeper
// regression: a NOOP action ("nothing changed, nothing to plan") must
// never count toward SyncProgress's Total/Done/Failed — those exist
// specifically to answer "did dependency X's content sync correctly",
// and a NOOP conveys nothing about that. Live-found: a real re-sync of
// two dependencies whose real SYNC_VERSION attempt had just failed
// replanned as two NOOP actions (their VersionReference hadn't
// changed), and progress.Finish being called unconditionally for every
// action kind reported that as "2 of 2 done, 0 failed" — indistinguishable
// from a genuine success.
func TestRunSyncNoopActionsDoNotCountAsProgress(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	root := scanDepFixture(t)

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

	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var projectID string
	for _, p := range projects {
		if p.Root == root {
			projectID = p.ID
		}
	}
	if projectID == "" {
		t.Fatalf("scanned project %s not found", root)
	}
	resolution, err := store.GetResolution(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if len(resolution.Dependencies) != 1 {
		t.Fatalf("resolution.Dependencies = %+v, want exactly 1", resolution.Dependencies)
	}
	dep := resolution.Dependencies[0]

	// Seed a reference that already matches the resolved version exactly
	// — the planner produces a NOOP for this dependency, not
	// ADD_REFERENCE+SYNC_VERSION (matching PLAN-003's own established
	// no-op fixture, TestSyncNoOpPlanMakesNoNetworkCalls).
	if err := store.AddReference(context.Background(), domain.VersionReference{
		ProjectID: projectID,
		Ecosystem: dep.Dependency.Ecosystem,
		Package:   dep.Dependency.Name,
		Version:   dep.Version,
		Reason:    domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	cfg, err := loadRagctlConfig()
	if err != nil {
		t.Fatalf("loadRagctlConfig: %v", err)
	}
	promoteFixtureVersion(t, store, dep, cfg.Vector.Backend)

	progress := &daemon.SyncProgress{}
	coordinator := daemon.NewBuildCoordinator()
	synced, failed, skipped, err := RunSync(context.Background(), coordinator, store, badgerStore, cfg, projectID, nil, false, false, false, io.Discard, nil, nil, nil, progress, nil, nil)
	if err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if synced != 0 || failed != 0 || skipped != 0 {
		t.Errorf("RunSync = synced=%d failed=%d skipped=%d, want all 0 (a genuine no-op)", synced, failed, skipped)
	}

	snap := progress.Snapshot()
	if snap.Total != 0 || snap.Done != 0 || snap.Failed != 0 {
		t.Errorf("progress after a NOOP-only run = %+v, want {Total:0 Done:0 Failed:0} — a NOOP must never look like a completed sync", snap)
	}
}

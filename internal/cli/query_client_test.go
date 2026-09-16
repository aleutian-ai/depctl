package cli

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/query"
)

// TestDaemonQueryServiceRoundTripsThroughRealDaemon is the actual proof
// WATCH-010's migration works: daemonQueryService (what `ragctl serve`
// now uses instead of opening a store itself) is exercised against a
// real, separately-spawned `ragctl daemon run` process — not an
// in-process fake — the same way an MCP client's serve process would
// reach it in practice.
func TestDaemonQueryServiceRoundTripsThroughRealDaemon(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	root := scanDepFixture(t) // registers example.com/app depending on example.com/foo (local replace)
	ctx := context.Background()

	c, err := ensureDaemon(ctx)
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	svc := &daemonQueryService{c: c}

	st, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.TotalProjects != 1 {
		t.Errorf("TotalProjects = %d, want 1", st.TotalProjects)
	}
	if len(st.Projects) != 1 || st.Projects[0].Root != root {
		t.Errorf("Projects = %+v, want one entry rooted at %s", st.Projects, root)
	}
	projectID := st.Projects[0].ID

	deps, err := svc.GetProjectDependencies(ctx, projectID)
	if err != nil {
		t.Fatalf("GetProjectDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].Dependency.Dependency.Name != "example.com/foo" {
		t.Errorf("GetProjectDependencies = %+v, want one dependency on example.com/foo", deps)
	}

	dv, err := svc.GetDependencyVersion(ctx, projectID, "example.com/foo")
	if err != nil {
		t.Fatalf("GetDependencyVersion: %v", err)
	}
	if dv.Dependency.Name != "example.com/foo" {
		t.Errorf("GetDependencyVersion = %+v, want example.com/foo", dv)
	}

	if _, err := svc.GetDependencyVersion(ctx, projectID, "example.com/does-not-exist"); err == nil {
		t.Error("GetDependencyVersion for an unknown package succeeded, want an error")
	}

	// None of the above needed a live embedder or vector backend —
	// confirms baseQueryService's split from fullQueryService actually
	// works: Status/GetProjectDependencies/GetDependencyVersion never
	// forced a dimension probe. SearchKnowledge is the one method that
	// does, and isn't exercised here since it needs a live embedder;
	// internal/query's own tests already cover its behavior against a
	// fake backend.
	_ = query.Query{} // documents that daemonQueryService.SearchKnowledge exists but isn't exercised by this test
}

// TestDaemonSyncTriggerRoundTripsThroughRealDaemon proves the
// sync_project MCP tool's write path also goes through the daemon's
// Scheduler now, rather than calling RunSync against a store serve
// opened itself.
func TestDaemonSyncTriggerRoundTripsThroughRealDaemon(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	scanDepFixture(t)
	ctx := context.Background()

	c, err := ensureDaemon(ctx)
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	svc := &daemonQueryService{c: c}
	st, err := svc.Status(ctx)
	if err != nil || len(st.Projects) != 1 {
		t.Fatalf("Status: %v, %+v", err, st)
	}

	// example.com/foo has no registry manifest match, so its one
	// SYNC_VERSION action fails at the embedder/vector-backend step (none
	// configured in this test env) rather than succeeding — that's
	// expected and fine. The point here isn't exercising a live backend;
	// it's proving the RPC itself reaches the daemon's real
	// Scheduler.Request/RunSync path at all (a transport-level failure
	// would surface as err != nil here, not a per-item failed count).
	trigger := &daemonSyncTrigger{c: c}
	var lines []string
	synced, failed, skipped, err := trigger.SyncProject(ctx, st.Projects[0].ID, "", func(line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	if synced != 0 || failed != 1 || skipped != 0 {
		t.Errorf("SyncProject = synced=%d failed=%d skipped=%d, want synced=0 failed=1 skipped=0 (the one action failed for lack of a backend, not the RPC itself)", synced, failed, skipped)
	}
	if len(lines) == 0 {
		t.Error("SyncProject's progress callback got no lines, want at least the FAIL line RunSync streams")
	}
}

// TestDaemonSyncTriggerDependencyFilterReachesRealSyncOptions is
// WATCH-019's wiring proof: dependency threads all the way from
// daemonSyncTrigger.SyncProject through a real api.SyncRequest into
// RunSync's own --dependency filter (internal/cli/sync.go), not just
// passed to a mock. A dependency name that matches nothing produces a
// genuine no-op (0/0/0) even though the project has one real,
// resolvable dependency that would otherwise fail at the backend step —
// proof the filter is actually being applied server-side, not ignored.
func TestDaemonSyncTriggerDependencyFilterReachesRealSyncOptions(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	scanDepFixture(t)
	ctx := context.Background()

	c, err := ensureDaemon(ctx)
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	svc := &daemonQueryService{c: c}
	st, err := svc.Status(ctx)
	if err != nil || len(st.Projects) != 1 {
		t.Fatalf("Status: %v, %+v", err, st)
	}

	trigger := &daemonSyncTrigger{c: c}
	synced, failed, skipped, err := trigger.SyncProject(ctx, st.Projects[0].ID, "does-not-exist", func(string) {})
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	if synced != 0 || failed != 0 || skipped != 0 {
		t.Errorf("SyncProject with a non-matching dependency filter = synced=%d failed=%d skipped=%d, want 0/0/0 (the filter should have excluded the project's one real dependency)", synced, failed, skipped)
	}
}

func TestLineWriterSplitsOnNewlines(t *testing.T) {
	var got []string
	w := lineWriter(func(line string) { got = append(got, line) })

	if _, err := w.Write([]byte("first\nsecond\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte("thi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte("rd\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLineWriterNilCallbackDiscardsSilently(t *testing.T) {
	w := lineWriter(nil)
	if _, err := w.Write([]byte("anything\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// TestDaemonPriorityBumperRoundTripsThroughRealDaemon is WATCH-020's
// wiring proof: daemonPriorityBumper reaches a real daemon's
// Scheduler.BumpPriority over the real /v1/sync/priority HTTP endpoint,
// not a mock. No sync is running for this project, so the real,
// correct answer is false — proving the round trip itself works
// without needing to orchestrate a genuinely slow background sync just
// to observe true.
func TestDaemonPriorityBumperRoundTripsThroughRealDaemon(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	scanDepFixture(t)
	ctx := context.Background()

	c, err := ensureDaemon(ctx)
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	svc := &daemonQueryService{c: c}
	st, err := svc.Status(ctx)
	if err != nil || len(st.Projects) != 1 {
		t.Fatalf("Status: %v, %+v", err, st)
	}

	bumper := &daemonPriorityBumper{c: c}
	bumped, err := bumper.BumpSyncPriority(ctx, st.Projects[0].ID, "example.com/foo")
	if err != nil {
		t.Fatalf("BumpSyncPriority: %v", err)
	}
	if bumped {
		t.Error("BumpSyncPriority = true with no sync running, want false")
	}
}

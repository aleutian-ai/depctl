package cli

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/symbolgraph"
)

// TestDaemonResolutionStoreRoundTripsThroughRealDaemon proves
// daemonResolutionStore (GRAPH-004's symbolgraph.ControlStore adapter,
// what explain_call_site's Resolver uses to match a resolved symbol
// against a project's real dependencies) actually reaches a live
// daemon's /v1/deps route and gets back a usable domain.Resolution —
// not just a mock. WATCH-019/020's own history is the reason this test
// exists at all: a wire-boundary adapter that only works against an
// in-process fake, never verified against the real daemon-over-HTTP
// path, silently broke that feature in every real deployment despite
// passing every unit test.
func TestDaemonResolutionStoreRoundTripsThroughRealDaemon(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	scanDepFixture(t) // registers example.com/app depending on example.com/foo (local replace)
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
	projectID := st.Projects[0].ID

	store := &daemonResolutionStore{c: c}
	resolution, err := store.GetResolution(ctx, projectID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if len(resolution.Dependencies) != 1 || resolution.Dependencies[0].Dependency.Name != "example.com/foo" {
		t.Errorf("GetResolution = %+v, want one dependency on example.com/foo", resolution)
	}

	// The actual point of this ticket's wiring: satisfies
	// symbolgraph.ControlStore end to end, over the real daemon.
	var _ symbolgraph.ControlStore = store
}

// TestDaemonResolutionStoreUnknownProjectPropagatesError proves an
// unknown project ID surfaces as a real error through the daemon round
// trip, not a silently empty Resolution — matching the same sentinel-
// identity concern WATCH-019/020 already established for this codebase
// (internal/cli/query_client.go's wrapQueryError).
func TestDaemonResolutionStoreUnknownProjectPropagatesError(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	ctx := context.Background()
	c, err := ensureDaemon(ctx)
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}

	store := &daemonResolutionStore{c: c}
	_, err = store.GetResolution(ctx, "proj_does_not_exist")
	if err == nil {
		t.Fatal("GetResolution for an unknown project succeeded, want an error")
	}
}

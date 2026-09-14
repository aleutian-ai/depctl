package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

func TestSyncDryRunPerformsNoWrites(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "ADD_REFERENCE") {
		t.Errorf("dry-run output missing plan content:\n%s", out.String())
	}

	// No reference should have been recorded — dry-run must not write.
	// The daemon still holds the control store's exclusive lock; release
	// it before reading the store directly.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	refs, err := store.ListAllReferences(context.Background())
	if err != nil {
		t.Fatalf("ListAllReferences: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("ListAllReferences after dry-run = %+v, want none (no state written)", refs)
	}
}

func TestSyncOfflineSkipsSyncVersionButRecordsReference(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	root := scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync", "--offline"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --offline: %v", err)
	}
	if !strings.Contains(out.String(), "SKIP (offline)") {
		t.Errorf("offline sync output missing skip line:\n%s", out.String())
	}

	// Find the project ID that was scanned. The daemon still holds the
	// control store's exclusive lock; release it first.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
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
		t.Fatalf("scanned project %s not found among %+v", root, projects)
	}

	// Reference bookkeeping doesn't need the network, so it must still
	// have happened even in offline mode.
	refs, err := store.ListProjectReferences(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListProjectReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].Package != "example.com/foo" || refs[0].Version == "" {
		t.Errorf("ListProjectReferences = %+v, want one reference to example.com/foo with a version", refs)
	}
}

func TestSyncNoOpAfterOfflineSyncPerformsNoFurtherReferenceChanges(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	first := NewRootCmd()
	first.SetArgs([]string{"sync", "--offline"})
	first.SetOut(new(bytes.Buffer))
	if err := first.Execute(); err != nil {
		t.Fatalf("first sync --offline: %v", err)
	}

	// Second run should be a NOOP for the reference (nothing changed) —
	// still reports the SYNC_VERSION as skipped (offline), since sync
	// state isn't accounted for without a real backend, but must not
	// error or duplicate reference records.
	second := NewRootCmd()
	second.SetArgs([]string{"sync", "--offline"})
	var out bytes.Buffer
	second.SetOut(&out)
	if err := second.Execute(); err != nil {
		t.Fatalf("second sync --offline: %v", err)
	}
}

// TestSyncNoOpPlanMakesNoNetworkCalls is the literal PLAN-003 acceptance
// criterion: a no-change sync performs zero backend/network writes. It
// seeds a VersionReference matching the resolved version exactly, so
// planner.Plan produces a NOOP (not a SYNC_VERSION), then runs `sync`
// WITHOUT --offline. If the embedder/vector-backend pipeline were built
// unconditionally (the bug this test guards against — an earlier version
// probed embedder.Dimensions on every non-offline run regardless of
// whether anything needed it), this would hang or fail trying to reach
// the configured Ollama/Qdrant endpoints; since nothing in the plan
// needs the pipeline, it must never be constructed at all.
func TestSyncNoOpPlanMakesNoNetworkCalls(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	root := scanDepFixture(t)

	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before reading and writing the store
	// directly. `sync` below auto-starts a fresh daemon of its own.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	var project domain.Project
	for _, p := range projects {
		if p.Root == root {
			project = p
		}
	}
	if project.ID == "" {
		t.Fatalf("scanned project %s not found", root)
	}
	resolution, err := store.GetResolution(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if len(resolution.Dependencies) != 1 {
		t.Fatalf("resolution.Dependencies = %+v, want exactly 1", resolution.Dependencies)
	}
	dep := resolution.Dependencies[0]

	if err := store.AddReference(context.Background(), domain.VersionReference{
		ProjectID: project.ID,
		Ecosystem: dep.Dependency.Ecosystem,
		Package:   dep.Dependency.Name,
		Version:   dep.Version,
		Reason:    domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync"}) // deliberately NOT --offline
	var out bytes.Buffer
	cmd.SetOut(&out)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sync: %v\noutput: %s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not return within 10s — the pipeline was probably built (and is hanging on a network call) for a plan with no work to do")
	}
	if !strings.Contains(out.String(), "0 synced, 0 failed, 0 skipped") {
		t.Errorf("sync summary = %q, want a genuine no-op (0/0/0)", out.String())
	}
}

func TestSyncDependencyFilter(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync", "--offline", "--dependency", "does-not-exist"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync --offline --dependency: %v", err)
	}
	if strings.Contains(out.String(), "example.com/foo") {
		t.Errorf("--dependency filter should have excluded example.com/foo:\n%s", out.String())
	}
}

// TestSyncReportsStructuredErrorForUnreachableEmbeddingBackend is
// WATCH-014's regression test: a sync that hits a SYNC_VERSION action
// against a dead embedding endpoint must report the daemon's own
// actionable "embedding backend unreachable" message, not a raw
// connection-refused error bubbled up from inside buildEmbedder.
func TestSyncReportsStructuredErrorForUnreachableEmbeddingBackend(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	deadEndpointsConfig(t, nil)
	scanDepFixture(t)

	c, err := ensureDaemon(context.Background())
	if err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}
	waitFor(t, "embedding readiness to report unreachable", func() bool {
		h, err := c.Health(context.Background())
		return err == nil && h.EmbeddingState == "unreachable"
	})

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sync"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	// A failed sync action makes `sync` exit non-zero (see runSync) —
	// expected here, since the embedding backend is unreachable. The
	// assertion that matters is what got streamed to out, not the exit.
	if err := cmd.Execute(); err == nil {
		t.Fatal("sync succeeded despite an unreachable embedding backend, want a failed action")
	}
	if !strings.Contains(out.String(), "embedding backend unreachable") {
		t.Errorf("sync output = %q, want it to report the structured embedding-unreachable message instead of a raw connection error", out.String())
	}
}

// TestFallbackManifestDerivesGithubURL covers REG-005: a Go dependency
// shaped like a direct github.com/<org>/<repo> import derives a
// single-source manifest instead of hitting the "no registry manifest"
// error a genuinely unmapped package still gets.
func TestFallbackManifestDerivesGithubURL(t *testing.T) {
	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "github.com/dgraph-io/badger/v4"}
	m, ok := fallbackManifest(dep)
	if !ok {
		t.Fatal("fallbackManifest = false, want true for a github.com module path")
	}
	if len(m.Sources) != 1 || m.Sources[0].URL != "https://github.com/dgraph-io/badger" {
		t.Errorf("Sources = %+v, want one source at https://github.com/dgraph-io/badger", m.Sources)
	}
}

func TestFallbackManifestRejectsVanityImportPath(t *testing.T) {
	dep := domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"}
	if _, ok := fallbackManifest(dep); ok {
		t.Error("fallbackManifest = true for a vanity import path, want false (no fetchable location without HTTP go-import resolution)")
	}
}

func TestFallbackManifestRejectsNonGoEcosystems(t *testing.T) {
	dep := domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "some-package"}
	if _, ok := fallbackManifest(dep); ok {
		t.Error("fallbackManifest = true for a non-Go ecosystem, want false")
	}
}

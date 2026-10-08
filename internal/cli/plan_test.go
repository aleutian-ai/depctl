package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// scanDepFixture runs `depctl scan` against a small go.mod fixture with
// one locally-replaced dependency (so it resolves fully offline, same
// pattern as TestDepsListsResolvedDependencies) and returns the
// registered project's root path. The dependency isn't in the registry
// (no manifest for "example.com/foo"), which is fine for these tests —
// planner.Plan still produces ADD_REFERENCE/SYNC_VERSION for an unmapped
// package, just flagged with a reason.
func scanDepFixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	// canonicalize now resolves symlinks (PROJ-002) — t.TempDir() on
	// macOS returns a path under the symlinked /var (-> /private/var), so
	// resolve it here too or callers comparing this returned root against
	// a scanned project's now-resolved Root would see a spurious mismatch.
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	writeGoMod(t, filepath.Join(base, "foolocal"), "module example.com/foo\n\ngo 1.21\n")
	root := filepath.Join(base, "app")
	writeGoMod(t, root, "module example.com/app\n\ngo 1.21\n\nrequire example.com/foo v0.0.0\n\nreplace example.com/foo => ../foolocal\n")

	scanCmd := NewRootCmd()
	scanCmd.SetArgs([]string{"scan", root})
	scanCmd.SetOut(new(bytes.Buffer))
	if err := scanCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return root
}

func TestPlanNewProjectShowsAddReferenceAndSyncVersion(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealDepctlBinary(t)
	scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"plan"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("plan: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "ADD_REFERENCE") {
		t.Errorf("plan output missing ADD_REFERENCE:\n%s", text)
	}
	if !strings.Contains(text, "SYNC_VERSION") {
		t.Errorf("plan output missing SYNC_VERSION:\n%s", text)
	}
	if !strings.Contains(text, "example.com/foo") {
		t.Errorf("plan output missing dependency name:\n%s", text)
	}
}

func TestPlanNeverWritesState(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealDepctlBinary(t)
	scanDepFixture(t)

	// Run plan twice; if it wrote any VersionReference or generation
	// state, the second run's diff would differ from the first (e.g.
	// ADD_REFERENCE would disappear once a reference exists).
	run := func() string {
		cmd := NewRootCmd()
		cmd.SetArgs([]string{"plan"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("plan: %v", err)
		}
		return out.String()
	}

	first := run()
	second := run()
	if first != second {
		t.Errorf("plan output changed between runs (plan must be read-only):\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestPlanJSONRoundTrips(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealDepctlBinary(t)
	scanDepFixture(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"plan", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	var plans []projectPlan
	if err := json.Unmarshal(out.Bytes(), &plans); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput: %s", err, out.String())
	}
	if len(plans) != 1 {
		t.Fatalf("got %d project plans, want 1", len(plans))
	}
	if len(plans[0].Actions) == 0 {
		t.Error("expected at least one action for a freshly scanned project")
	}
}

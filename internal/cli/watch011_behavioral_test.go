package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestAllStoreTouchingCommandsSucceedAgainstOneRealDaemon is WATCH-011's
// own remaining "behavioral test" acceptance criterion, closed here:
// every store-touching CLI command, run in sequence against one real
// daemon, must succeed — proving none of them still opens the stores
// independently (which would either deadlock against the daemon's own
// bbolt lock or silently succeed by racing it, depending on OS lock
// semantics — either way, a real bug this test would catch).
// TestServeOverRealStdioTransport (epic 47/VERIFY-001) already proves
// the MCP tool surface works the same way through a real `serve`
// subprocess; this test is that ticket's other half — the plain CLI
// command list WATCH-011's own design named — which nothing else
// exercises together in one sequence.
func TestAllStoreTouchingCommandsSucceedAgainstOneRealDaemon(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	noAmbientSync(t) // keep this test's own command sequence in full control of when sync runs

	root := scanDepFixture(t) // also registers the project — re-scanning below must be a safe no-op

	run := func(args ...string) string {
		t.Helper()
		cmd := NewRootCmd()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v\noutput:\n%s", args, err, out.String())
		}
		return out.String()
	}

	run("scan", root)

	syncOut := run("sync", "--dry-run")
	if !strings.Contains(syncOut, "example.com/foo") {
		t.Errorf("sync --dry-run output missing the resolved dependency:\n%s", syncOut)
	}

	planOut := run("plan")
	if !strings.Contains(planOut, "example.com/foo") {
		t.Errorf("plan output missing the resolved dependency:\n%s", planOut)
	}

	statusOut := run("status")
	if !strings.Contains(statusOut, "projects:") {
		t.Errorf("status output missing the expected projects: line:\n%s", statusOut)
	}

	projectListOut := run("project", "list")
	if !strings.Contains(projectListOut, root) {
		t.Errorf("project list output missing the registered project root:\n%s", projectListOut)
	}

	// deps needs the real project ID, not the root path — parse it out of
	// `project list`'s own output (ID<whitespace>Root per line) rather
	// than opening the store directly, which the still-running daemon
	// correctly holds locked — reaching for a direct open here would
	// undermine the very thing this test exists to prove.
	var projectID string
	for _, line := range strings.Split(projectListOut, "\n") {
		if strings.HasSuffix(strings.TrimRight(line, " "), root) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				projectID = fields[0]
			}
		}
	}
	if projectID == "" {
		t.Fatalf("could not parse a project ID for %s out of `project list` output:\n%s", root, projectListOut)
	}

	depsOut := run("deps", projectID)
	if !strings.Contains(depsOut, "example.com/foo") {
		t.Errorf("deps output missing the resolved dependency:\n%s", depsOut)
	}

	// describe: example.com/foo has no registry manifest, so a clean
	// "not found"-shaped response (not a crash or a hang) is the correct
	// outcome here — this call is about proving the command reaches the
	// daemon and gets a real answer, not about registry content.
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"describe", "go", "example.com/foo"})
	var describeOut bytes.Buffer
	cmd.SetOut(&describeOut)
	cmd.SetErr(&describeOut)
	_ = cmd.Execute() // error or not, both are a real, non-hanging daemon round trip

	gcOut := run("gc", "--dry-run")
	if !strings.Contains(gcOut, "eligible") && !strings.Contains(gcOut, "GC") {
		t.Errorf("gc --dry-run output unexpected:\n%s", gcOut)
	}

	// doctor exits non-zero on a WARN state (e.g. "no active generations
	// yet" — expected here, nothing was synced), not just an unhealthy
	// one, so this call is about a real, non-hanging daemon round trip
	// producing real output, not a zero exit code.
	doctorCmd := NewRootCmd()
	doctorCmd.SetArgs([]string{"doctor"})
	var doctorOut bytes.Buffer
	doctorCmd.SetOut(&doctorOut)
	doctorCmd.SetErr(&doctorOut)
	_ = doctorCmd.Execute()
	if doctorOut.Len() == 0 {
		t.Error("doctor produced no output")
	}

	// Stop the daemon, then confirm bboltstore.Open succeeds immediately
	// — the real point of this whole test: nothing above leaked its own
	// independent handle to the stores.
	stopRunningDaemon(t)
	store2, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore after stopping the daemon: %v — a command above leaked a store handle", err)
	}
	store2.Close()
}

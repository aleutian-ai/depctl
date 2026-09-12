package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestDescribeRunsThroughRealDaemon proves `ragctl describe` no longer
// opens the store itself: it goes through ensureDaemon like every other
// migrated command, against a real, separately-spawned daemon process.
func TestDescribeRunsThroughRealDaemon(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	scanDepFixture(t) // also auto-starts the daemon this test relies on

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"describe"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("describe: %v", err)
	}
	// Scanning alone doesn't add a reference (only sync does), so a
	// fleet-wide describe has no rows for this fixture's dependency yet
	// — the point here is proving the command reached the daemon and
	// got a real report back at all, not any specific row.
	if !strings.Contains(out.String(), "manifest(s) loaded") {
		t.Errorf("describe output missing the registry summary line:\n%s", out.String())
	}
}

// TestDoctorUsesDaemonWhenReachable proves doctor's daemon-mediated path
// actually works end-to-end: while a real daemon holds the stores open
// (confirmed by a direct open attempt failing with ErrLocked), `ragctl
// doctor` still succeeds — because it never tries to open them itself
// when a daemon answers.
func TestDoctorUsesDaemonWhenReachable(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	scanDepFixture(t) // also auto-starts the daemon this test relies on

	// Confirm the daemon genuinely holds the exclusive lock right now —
	// otherwise this test wouldn't prove doctor avoided opening it too.
	if _, err := openControlStore(); err == nil {
		t.Fatal("expected the running daemon to hold control.db's lock")
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor"})
	err := cmd.Execute()
	// Some checks (embedding/backend reachability) are expected to fail
	// in this offline test env — that's fine and unrelated to what this
	// test verifies. What matters: doctor produced a real report at all,
	// meaning it got its data from the daemon rather than failing to
	// open a locked store itself.
	if err != nil {
		if _, ok := err.(ExitCodeError); !ok {
			t.Fatalf("doctor: %v", err)
		}
	}
	if !strings.Contains(out.String(), "control DB (bbolt) open") {
		t.Fatalf("doctor output missing expected check:\n%s", out.String())
	}
	if strings.Contains(out.String(), "locked") {
		t.Errorf("doctor's control DB check reported a lock error even though the daemon should have served this check, not a direct open:\n%s", out.String())
	}
}

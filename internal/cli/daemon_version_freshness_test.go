package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionStaleWarning(t *testing.T) {
	if got := versionStaleWarning(123, "abc123", "abc123"); got != "" {
		t.Errorf("matching versions = %q, want no warning", got)
	}
	if got := versionStaleWarning(123, "", "abc123"); got != "" {
		t.Errorf("daemon reports empty version = %q, want no warning (nothing reliable to compare)", got)
	}
	if got := versionStaleWarning(123, "unknown", "abc123"); got != "" {
		t.Errorf("daemon reports \"unknown\" = %q, want no warning (build info genuinely unavailable, not a real mismatch signal)", got)
	}
	if got := versionStaleWarning(123, "abc123", "unknown"); got != "" {
		t.Errorf("this command's own version is \"unknown\" = %q, want no warning — e.g. a `go test` binary, which isn't VCS-stamped the same way `go build` is; this exact gap was a live-found bug in this fix's own first version", got)
	}
	got := versionStaleWarning(123, "abc123", "def456")
	if got == "" || !strings.Contains(got, "123") || !strings.Contains(got, "abc123") || !strings.Contains(got, "def456") {
		t.Errorf("mismatched versions = %q, want a warning naming the pid and both versions", got)
	}
}

func TestVersionFreshnessLabel(t *testing.T) {
	prev := ragctlVersion
	ragctlVersion = "def456"
	defer func() { ragctlVersion = prev }()

	if got := versionFreshnessLabel("def456"); got != "def456" {
		t.Errorf("matching version label = %q, want the bare version, no staleness note", got)
	}
	if got := versionFreshnessLabel("unknown"); got != "unknown" {
		t.Errorf("unknown version label = %q, want the bare \"unknown\", no staleness note", got)
	}
	got := versionFreshnessLabel("abc123")
	if !strings.Contains(got, "abc123") || !strings.Contains(got, "stale") || !strings.Contains(got, "def456") {
		t.Errorf("mismatched version label = %q, want it to name both versions and say stale", got)
	}
}

// TestDaemonStatusFlagsStaleVersion is the real end-to-end proof: the
// daemon is a long-running process reused by every later command
// (ADR-011), so a rebuild after it started must be detectable, not
// silently ignored the way the pre-fix hardcoded "v0.1.0" made
// impossible for every build to ever disagree with itself. Simulates a
// rebuild by swapping the current-binary's own version after the daemon
// has already started and reported its own — exactly what ensureDaemon/
// `daemon status`/`doctor` see for a real upgraded binary talking to an
// old daemon.
func TestDaemonStatusFlagsStaleVersion(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	if _, err := ensureDaemon(t.Context()); err != nil {
		t.Fatalf("ensureDaemon: %v", err)
	}

	statusCmd := func() string {
		cmd := NewRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"daemon", "status"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("daemon status: %v", err)
		}
		return out.String()
	}

	if got := statusCmd(); !strings.Contains(got, "version:") || strings.Contains(got, "stale") {
		t.Fatalf("status before any rebuild should report a current version, got:\n%s", got)
	}

	// The daemon subprocess was spawned from useRealRagctlBinary's own
	// binary and already reported its real build version; simulate this
	// process itself now being a different (upgraded) build.
	prev := ragctlVersion
	ragctlVersion = "a-totally-different-build"
	t.Cleanup(func() { ragctlVersion = prev })

	got := statusCmd()
	if !strings.Contains(got, "stale") {
		t.Errorf("status after a simulated rebuild should report the daemon's version as stale, got:\n%s", got)
	}

	doctorCmd := NewRootCmd()
	var doctorOut bytes.Buffer
	doctorCmd.SetOut(&doctorOut)
	doctorCmd.SetArgs([]string{"doctor"})
	_ = doctorCmd.Execute() // may return ExitCodeError for unrelated checks (offline backend); irrelevant here
	if !strings.Contains(doctorOut.String(), "daemon build matches this command") || !strings.Contains(doctorOut.String(), "WARN") {
		t.Errorf("doctor output missing the stale-version warning:\n%s", doctorOut.String())
	}
}

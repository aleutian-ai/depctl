package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/config"
)

// TestDaemonStatusFlagsStaleConfig is the real end-to-end proof for the
// config-drift warning: config is loaded once for the daemon's whole
// lifetime, so an edit after it started must be detectable, not silently
// ignored.
func TestDaemonStatusFlagsStaleConfig(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealDepctlBinary(t)

	// Start the daemon with today's config.
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

	if got := statusCmd(); !strings.Contains(got, "config:") || strings.Contains(got, "stale") {
		t.Fatalf("status before any config edit should report current config, got:\n%s", got)
	}

	// Edit config.yaml on disk without restarting the daemon.
	writeTestConfig(t, func(c *config.Config) { c.Embedding.Model = "a-totally-different-model" })

	got := statusCmd()
	if !strings.Contains(got, "stale") {
		t.Errorf("status after an unresynced config edit should report stale, got:\n%s", got)
	}

	// doctor's daemon-mediated path must catch the same drift.
	doctorCmd := NewRootCmd()
	var doctorOut bytes.Buffer
	doctorCmd.SetOut(&doctorOut)
	doctorCmd.SetArgs([]string{"doctor"})
	_ = doctorCmd.Execute() // may return ExitCodeError for unrelated checks (offline backend); irrelevant here
	if !strings.Contains(doctorOut.String(), "config matches running daemon") || !strings.Contains(doctorOut.String(), "WARN") {
		t.Errorf("doctor output missing the stale-config warning:\n%s", doctorOut.String())
	}
}

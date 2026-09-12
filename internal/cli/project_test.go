package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestProjectListEmpty(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	rootCmd := NewRootCmd()
	rootCmd.SetArgs([]string{"project", "list"})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("project list: %v", err)
	}
	if !strings.Contains(out.String(), "no registered projects") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestProjectListAndShowAfterScan(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	writeGoMod(t, root, "module example.com/showme\n\ngo 1.21\n")

	scanCmd := NewRootCmd()
	scanCmd.SetArgs([]string{"scan", root})
	scanCmd.SetOut(new(bytes.Buffer))
	if err := scanCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// `project list`/`show` open the control store directly (they haven't
	// moved to the daemon); release the lock the auto-started daemon
	// still holds.
	stopRunningDaemon(t)

	listCmd := NewRootCmd()
	listCmd.SetArgs([]string{"project", "list"})
	var listOut bytes.Buffer
	listCmd.SetOut(&listOut)
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("project list: %v", err)
	}
	if !strings.Contains(listOut.String(), root) {
		t.Fatalf("project list missing %s:\n%s", root, listOut.String())
	}

	fields := strings.Fields(strings.TrimSpace(listOut.String()))
	if len(fields) < 1 {
		t.Fatalf("could not parse project ID from list output: %q", listOut.String())
	}
	id := fields[0]

	showCmd := NewRootCmd()
	showCmd.SetArgs([]string{"project", "show", id})
	var showOut bytes.Buffer
	showCmd.SetOut(&showOut)
	if err := showCmd.Execute(); err != nil {
		t.Fatalf("project show: %v", err)
	}
	if !strings.Contains(showOut.String(), "Resolution: go, 0 dependencies") {
		t.Errorf("unexpected show output:\n%s", showOut.String())
	}
}

func TestProjectShowNotFound(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"project", "show", "proj_missing"})
	cmd.SetOut(new(bytes.Buffer))
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing project, got nil")
	}
	if !strings.Contains(err.Error(), "no registered project") {
		t.Errorf("unexpected error: %v", err)
	}
}

// runInitForTest runs `ragctl init` against the already-isolated
// environment, failing the test on error.
func runInitForTest(t *testing.T) {
	t.Helper()
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
}

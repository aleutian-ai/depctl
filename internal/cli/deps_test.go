package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestDepsUnknownProject(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"deps", "proj_missing"})
	cmd.SetOut(new(bytes.Buffer))
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown project, got nil")
	}
	if !strings.Contains(err.Error(), "no registered project") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDepsListsResolvedDependencies(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)

	base := t.TempDir()
	writeGoMod(t, filepath.Join(base, "foolocal"), "module example.com/foo\n\ngo 1.21\n")
	root := filepath.Join(base, "main")
	writeGoMod(t, root, "module example.com/main\n\ngo 1.21\n\nrequire example.com/foo v0.0.0\n\nreplace example.com/foo => ../foolocal\n")

	scanCmd := NewRootCmd()
	scanCmd.SetArgs([]string{"scan", root})
	scanCmd.SetOut(new(bytes.Buffer))
	if err := scanCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	listCmd := NewRootCmd()
	listCmd.SetArgs([]string{"project", "list"})
	var listOut bytes.Buffer
	listCmd.SetOut(&listOut)
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("project list: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(listOut.String()))
	if len(fields) < 1 {
		t.Fatalf("could not parse project ID: %q", listOut.String())
	}
	id := fields[0]

	depsCmd := NewRootCmd()
	depsCmd.SetArgs([]string{"deps", id})
	var depsOut bytes.Buffer
	depsCmd.SetOut(&depsOut)
	if err := depsCmd.Execute(); err != nil {
		t.Fatalf("deps: %v", err)
	}
	if !strings.Contains(depsOut.String(), "example.com/foo") {
		t.Errorf("unexpected deps output:\n%s", depsOut.String())
	}
}

func TestDepsNoResolutionYet(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)

	// A project can be registered without ever having been resolved
	// (e.g. its ecosystem isn't supported yet) — deps must say so clearly
	// rather than treating it the same as an unknown project.
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	if err := store.PutProject(t.Context(), domain.Project{ID: "proj_no_resolution", Root: "/tmp/foo"}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"deps", "proj_no_resolution"})
	cmd.SetOut(new(bytes.Buffer))
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no resolution") {
		t.Errorf("unexpected error: %v", err)
	}
}

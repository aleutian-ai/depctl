package cli

import (
	"strings"
	"testing"
)

func TestUnimplementedCommandsFailLoudly(t *testing.T) {
	cmds := []string{"backend"}
	for _, name := range cmds {
		t.Run(name, func(t *testing.T) {
			root := NewRootCmd()
			root.SetArgs([]string{name})
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for %q, got nil", name)
			}
			if !strings.Contains(err.Error(), "feature not implemented in this build") {
				t.Fatalf("unexpected error message for %q: %v", name, err)
			}
		})
	}
}

func TestRootHelpListsAllCommands(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"--help"})
	root.SetOut(new(strings.Builder))
	if err := root.Execute(); err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	want := []string{"init", "scan", "project", "deps", "plan", "sync", "status", "doctor", "gc", "watch", "serve", "backend", "registry", "config"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("missing top-level command %q", w)
		}
	}
}

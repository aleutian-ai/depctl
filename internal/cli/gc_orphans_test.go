package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestGCOrphansDryRunListsCandidateWithoutDeleting(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealDepctlBinary(t)

	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	gen := domain.Generation{
		ID:         "gen_orphan_test",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/orphan"}, Version: "v1.0.0"},
		State:      domain.GenFailed,
	}
	if err := store.PutGeneration(context.Background(), gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--orphans", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --orphans --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/orphan") || !strings.Contains(out.String(), "gen_orphan_test") {
		t.Errorf("output missing expected orphan candidate:\n%s", out.String())
	}

	// --dry-run must not touch state.
	stopRunningDaemon(t)
	store2, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store2.Close()
	got, err := store2.GetGeneration(context.Background(), "gen_orphan_test")
	if err != nil {
		t.Fatalf("GetGeneration after dry-run: %v", err)
	}
	if got.ID != "gen_orphan_test" {
		t.Errorf("generation record gone after --dry-run, want it untouched")
	}
}

func TestGCWithoutOrphansFlagIgnoresOrphanCandidates(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealDepctlBinary(t)

	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	gen := domain.Generation{
		ID:         "gen_orphan_test2",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/orphan2"}, Version: "v1.0.0"},
		State:      domain.GenFailed,
	}
	if err := store.PutGeneration(context.Background(), gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--dry-run"}) // no --orphans
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --dry-run: %v", err)
	}
	if strings.Contains(out.String(), "example.com/orphan2") {
		t.Errorf("plain `depctl gc` (no --orphans) reported an orphan generation — the two paths must stay separate:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nothing eligible") {
		t.Errorf("output = %q, want \"nothing eligible\" — orphan generations are invisible to the reference-based path", out.String())
	}
}

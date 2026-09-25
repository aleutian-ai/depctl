package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestGCSupersededDuplicatesDryRunListsCandidateWithoutDeleting(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	active := domain.Generation{
		ID:         "gen_dup_active_test",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/dupwidget"}, Version: "v1.0.0"},
		State:      domain.GenActive,
	}
	dup := domain.Generation{
		ID:         "gen_dup_test",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/dupwidget"}, Version: "v1.0.0"},
		State:      domain.GenSuperseded,
	}
	if err := store.PutGeneration(context.Background(), active); err != nil {
		t.Fatalf("PutGeneration active: %v", err)
	}
	if err := store.PutGeneration(context.Background(), dup); err != nil {
		t.Fatalf("PutGeneration dup: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--superseded-duplicates", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --superseded-duplicates --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "example.com/dupwidget") || !strings.Contains(out.String(), "gen_dup_test") {
		t.Errorf("output missing expected duplicate candidate:\n%s", out.String())
	}
	if strings.Contains(out.String(), "gen_dup_active_test") {
		t.Errorf("output listed the ACTIVE generation as a candidate:\n%s", out.String())
	}

	// --dry-run must not touch state.
	stopRunningDaemon(t)
	store2, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store2.Close()
	got, err := store2.GetGeneration(context.Background(), "gen_dup_test")
	if err != nil {
		t.Fatalf("GetGeneration after dry-run: %v", err)
	}
	if got.ID != "gen_dup_test" {
		t.Errorf("generation record gone after --dry-run, want it untouched")
	}
}

func TestGCWithoutSupersededDuplicatesFlagIgnoresDuplicateCandidates(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	active := domain.Generation{
		ID:         "gen_dup_active_test2",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/dupwidget2"}, Version: "v1.0.0"},
		State:      domain.GenActive,
	}
	dup := domain.Generation{
		ID:         "gen_dup_test2",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/dupwidget2"}, Version: "v1.0.0"},
		State:      domain.GenSuperseded,
	}
	if err := store.PutGeneration(context.Background(), active); err != nil {
		t.Fatalf("PutGeneration active: %v", err)
	}
	if err := store.PutGeneration(context.Background(), dup); err != nil {
		t.Fatalf("PutGeneration dup: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--dry-run"}) // no --superseded-duplicates
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --dry-run: %v", err)
	}
	if strings.Contains(out.String(), "example.com/dupwidget2") {
		t.Errorf("plain `ragctl gc` (no --superseded-duplicates) reported a duplicate generation — the paths must stay separate:\n%s", out.String())
	}
}

func TestGCRejectsCombiningOrphansAndSupersededDuplicates(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--orphans", "--superseded-duplicates", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatal("gc --orphans --superseded-duplicates: want an error, these are separate passes")
	}
}

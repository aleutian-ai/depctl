package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

func TestGCReportsNothingEligibleWhenStoreIsEmpty(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "nothing eligible") {
		t.Errorf("output = %q, want \"nothing eligible\"", out.String())
	}
}

func TestGCDryRunListsCandidateWithoutDeleting(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	if err := store.AddReference(context.Background(), domain.VersionReference{
		ProjectID: domain.GracePeriodProjectID, Ecosystem: domain.EcosystemGo, Package: "old/pkg", Version: "v0.1.0",
		Reason: domain.ReferenceReasonGracePeriod, LastSeenAt: time.Now().Add(-1000 * time.Hour),
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "old/pkg") || !strings.Contains(out.String(), "v0.1.0") {
		t.Errorf("output missing expected candidate:\n%s", out.String())
	}

	// --dry-run must not touch state. Stop the daemon `gc` auto-started
	// first: it still holds the control store's exclusive lock.
	stopRunningDaemon(t)
	store2, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store2.Close()
	refs, err := store2.ListReferences(context.Background(), domain.EcosystemGo, "old/pkg", "v0.1.0")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("ListReferences after dry-run = %+v, want the reference still present (untouched)", refs)
	}
}

func TestGCSkipsCandidatesStillProtectedByReferenceOrPin(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	if err := store.AddReference(context.Background(), domain.VersionReference{
		ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "in-use/pkg", Version: "v1.0.0", Reason: domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	store.Close()

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"gc", "--dry-run"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gc --dry-run: %v", err)
	}
	if strings.Contains(out.String(), "in-use/pkg") {
		t.Errorf("actively-referenced package reported as a GC candidate:\n%s", out.String())
	}
}

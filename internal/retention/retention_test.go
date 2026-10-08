package retention

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/domain"
)

func openTestStore(t *testing.T) *bbolt.Store {
	t.Helper()
	s, err := bbolt.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDropReferenceAddsGracePeriodWhenLastReferenceRemoved(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	if err := store.AddReference(ctx, domain.VersionReference{
		ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	if err := DropReference(ctx, store, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "proj_1"); err != nil {
		t.Fatalf("DropReference: %v", err)
	}

	refs, err := store.ListReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].Reason != domain.ReferenceReasonGracePeriod {
		t.Fatalf("refs = %+v, want exactly one grace_period reference", refs)
	}
	if refs[0].ProjectID != domain.GracePeriodProjectID {
		t.Errorf("grace reference ProjectID = %s, want %s", refs[0].ProjectID, domain.GracePeriodProjectID)
	}
}

func TestDropReferenceDoesNotAddGraceWhenAnotherProjectStillReferencesIt(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	for _, p := range []string{"proj_1", "proj_2"} {
		if err := store.AddReference(ctx, domain.VersionReference{
			ProjectID: p, Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject,
		}); err != nil {
			t.Fatalf("AddReference(%s): %v", p, err)
		}
	}

	if err := DropReference(ctx, store, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "proj_1"); err != nil {
		t.Fatalf("DropReference: %v", err)
	}

	refs, err := store.ListReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].ProjectID != "proj_2" {
		t.Fatalf("refs = %+v, want only proj_2's reference (no grace added while another project depends on it)", refs)
	}
}

func TestDropReferenceDoesNotAddGraceWhenLatestOrPinExists(t *testing.T) {
	for _, reason := range []domain.ReferenceReason{domain.ReferenceReasonLatest, domain.ReferenceReasonManualPin} {
		t.Run(string(reason), func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t)

			if err := store.AddReference(ctx, domain.VersionReference{
				ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject,
			}); err != nil {
				t.Fatalf("AddReference (project): %v", err)
			}
			if err := store.AddReference(ctx, domain.VersionReference{
				ProjectID: "_policy", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: reason,
			}); err != nil {
				t.Fatalf("AddReference (%s): %v", reason, err)
			}

			if err := DropReference(ctx, store, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "proj_1"); err != nil {
				t.Fatalf("DropReference: %v", err)
			}

			refs, err := store.ListReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
			if err != nil {
				t.Fatalf("ListReferences: %v", err)
			}
			for _, r := range refs {
				if r.Reason == domain.ReferenceReasonGracePeriod {
					t.Errorf("grace_period reference added despite an active %s reference: %+v", reason, refs)
				}
			}
		})
	}
}

func TestEffectiveGracePeriodDefaultsMissingOrZeroTo14Days(t *testing.T) {
	for _, configured := range []time.Duration{0, -1 * time.Hour} {
		got := EffectiveGracePeriod(configured)
		if got != 336*time.Hour {
			t.Errorf("EffectiveGracePeriod(%v) = %v, want 336h (RET-002's documented default)", configured, got)
		}
	}
}

func TestEffectiveGracePeriodRespectsConfiguredValue(t *testing.T) {
	got := EffectiveGracePeriod(72 * time.Hour)
	if got != 72*time.Hour {
		t.Errorf("EffectiveGracePeriod(72h) = %v, want 72h", got)
	}
}

func TestGraceExpiryComputation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		gracePeriod time.Duration
	}{
		{"zero", 0},
		{"default-14-days", 336 * time.Hour},
		{"custom", 72 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := domain.VersionReference{LastSeenAt: base}
			want := base.Add(tc.gracePeriod)
			got := GraceExpiry(ref, tc.gracePeriod)
			if !got.Equal(want) {
				t.Errorf("GraceExpiry = %v, want %v", got, want)
			}
		})
	}
}

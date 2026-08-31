package bbolt

import (
	"context"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

func fixedTime(seconds int64) time.Time {
	return time.Unix(seconds, 0).UTC()
}

func TestAddReferenceAndCount(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	ref := domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject}
	if err := store.AddReference(ctx, ref); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	count, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if count != 1 {
		t.Errorf("CountReferences = %d, want 1", count)
	}
}

func TestTwoProjectsReferencingSameVersionCreateTwoReferences(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	for _, projectID := range []string{"proj_1", "proj_2"} {
		ref := domain.VersionReference{ProjectID: projectID, Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject}
		if err := store.AddReference(ctx, ref); err != nil {
			t.Fatalf("AddReference(%s): %v", projectID, err)
		}
	}

	count, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if count != 2 {
		t.Errorf("CountReferences = %d, want 2", count)
	}
}

func TestRemovingOneProjectReferenceLeavesTheOther(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	for _, projectID := range []string{"proj_1", "proj_2"} {
		ref := domain.VersionReference{ProjectID: projectID, Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject}
		if err := store.AddReference(ctx, ref); err != nil {
			t.Fatalf("AddReference(%s): %v", projectID, err)
		}
	}

	if err := store.RemoveReference(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "proj_1"); err != nil {
		t.Fatalf("RemoveReference: %v", err)
	}

	count, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if count != 1 {
		t.Errorf("CountReferences = %d, want 1 (proj_1 removed, not both)", count)
	}

	refs, err := store.ListReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	if len(refs) != 1 || refs[0].ProjectID != "proj_2" {
		t.Errorf("ListReferences = %+v, want only proj_2", refs)
	}
}

func TestRemovingLastReferenceLeavesCountZero(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	ref := domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject}
	if err := store.AddReference(ctx, ref); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	if err := store.RemoveReference(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "proj_1"); err != nil {
		t.Fatalf("RemoveReference: %v", err)
	}

	count, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if count != 0 {
		t.Errorf("CountReferences = %d, want 0", count)
	}
}

func TestRemoveReferenceOfAbsentReferenceIsNoOp(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if err := store.RemoveReference(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "proj_never_existed"); err != nil {
		t.Errorf("RemoveReference (absent): %v", err)
	}
}

func TestAddReferenceIsIdempotentAndPreservesFirstSeenAt(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	first := domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject, FirstSeenAt: fixedTime(1), LastSeenAt: fixedTime(1)}
	if err := store.AddReference(ctx, first); err != nil {
		t.Fatalf("AddReference (first): %v", err)
	}
	second := first
	second.FirstSeenAt = fixedTime(2)
	second.LastSeenAt = fixedTime(2)
	if err := store.AddReference(ctx, second); err != nil {
		t.Fatalf("AddReference (second): %v", err)
	}

	count, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if count != 1 {
		t.Errorf("CountReferences = %d, want 1 (idempotent upsert, not a duplicate)", count)
	}

	refs, err := store.ListReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("ListReferences = %+v, want 1", refs)
	}
	if !refs[0].FirstSeenAt.Equal(fixedTime(1)) {
		t.Errorf("FirstSeenAt = %v, want preserved from first Add (%v)", refs[0].FirstSeenAt, fixedTime(1))
	}
	if !refs[0].LastSeenAt.Equal(fixedTime(2)) {
		t.Errorf("LastSeenAt = %v, want refreshed to second Add (%v)", refs[0].LastSeenAt, fixedTime(2))
	}
}

func TestReferencesPersistAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/control.db"

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ref := domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject}
	if err := s.AddReference(ctx, ref); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	count, err := s2.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences after reopen: %v", err)
	}
	if count != 1 {
		t.Errorf("CountReferences after reopen = %d, want 1", count)
	}
}

func TestListProjectReferencesFiltersToProjectReasonOnly(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	refs := []domain.VersionReference{
		{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject},
		{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "github.com/pkg/errors", Version: "v0.9.1", Reason: domain.ReferenceReasonProject},
		{ProjectID: domain.GracePeriodProjectID, Ecosystem: domain.EcosystemGo, Package: "old/pkg", Version: "v0.1.0", Reason: domain.ReferenceReasonGracePeriod},
		{ProjectID: "proj_2", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.60.0", Reason: domain.ReferenceReasonProject},
	}
	for _, r := range refs {
		if err := store.AddReference(ctx, r); err != nil {
			t.Fatalf("AddReference: %v", err)
		}
	}

	got, err := store.ListProjectReferences(ctx, "proj_1")
	if err != nil {
		t.Fatalf("ListProjectReferences: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListProjectReferences = %+v, want 2 (proj_1's own project-reason refs only)", got)
	}
	for _, r := range got {
		if r.ProjectID != "proj_1" || r.Reason != domain.ReferenceReasonProject {
			t.Errorf("unexpected reference in proj_1's list: %+v", r)
		}
	}
}

func TestListAllReferencesReturnsEverything(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	refs := []domain.VersionReference{
		{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject},
		{ProjectID: "proj_2", Ecosystem: domain.EcosystemPython, Package: "requests", Version: "v2.32.0", Reason: domain.ReferenceReasonProject},
	}
	for _, r := range refs {
		if err := store.AddReference(ctx, r); err != nil {
			t.Fatalf("AddReference: %v", err)
		}
	}

	all, err := store.ListAllReferences(ctx)
	if err != nil {
		t.Fatalf("ListAllReferences: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAllReferences = %+v, want 2", all)
	}
}

func TestDeleteAllReferencesRemovesEveryReasonForOneVersionOnly(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	refs := []domain.VersionReference{
		{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonProject},
		{ProjectID: domain.GracePeriodProjectID, Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0", Reason: domain.ReferenceReasonGracePeriod},
		{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.68.0", Reason: domain.ReferenceReasonProject},
	}
	for _, r := range refs {
		if err := store.AddReference(ctx, r); err != nil {
			t.Fatalf("AddReference: %v", err)
		}
	}

	if err := store.DeleteAllReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0"); err != nil {
		t.Fatalf("DeleteAllReferences: %v", err)
	}

	count, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if count != 0 {
		t.Errorf("CountReferences for v1.67.0 = %d, want 0", count)
	}

	other, err := store.CountReferences(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.68.0")
	if err != nil {
		t.Fatalf("CountReferences: %v", err)
	}
	if other != 1 {
		t.Errorf("CountReferences for v1.68.0 = %d, want 1 (untouched)", other)
	}
}

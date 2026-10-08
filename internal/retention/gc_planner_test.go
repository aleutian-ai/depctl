package retention

import (
	"context"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/domain"
)

const testBackend = "qdrant"

var testNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func addGraceRef(t *testing.T, store *fakeStore, eco domain.Ecosystem, pkg, version string, lastSeenAt time.Time) {
	t.Helper()
	if err := store.AddReference(context.Background(), domain.VersionReference{
		ProjectID: domain.GracePeriodProjectID, Ecosystem: eco, Package: pkg, Version: version,
		Reason: domain.ReferenceReasonGracePeriod, LastSeenAt: lastSeenAt,
	}); err != nil {
		t.Fatalf("AddReference (grace): %v", err)
	}
}

func TestPlanGCVersionWithExpiredGraceAndNoReferencesIsEligible(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "old/pkg", "v0.1.0", testNow.Add(-400*time.Hour)) // well past 336h default

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Version != "v0.1.0" {
		t.Fatalf("candidates = %+v, want exactly one eligible version", candidates)
	}
}

func TestPlanGCVersionWithManualPinIsNeverEligible(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "pinned/pkg", "v1.0.0", testNow.Add(-1000*time.Hour))
	if err := store.AddReference(ctx, domain.VersionReference{
		ProjectID: "_pin", Ecosystem: domain.EcosystemGo, Package: "pinned/pkg", Version: "v1.0.0", Reason: domain.ReferenceReasonManualPin,
	}); err != nil {
		t.Fatalf("AddReference (pin): %v", err)
	}

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	for _, c := range candidates {
		if c.Package == "pinned/pkg" {
			t.Errorf("manually pinned version reported as GC-eligible: %+v", c)
		}
	}
}

// TestPlanGCOtherVersionActiveDoesNotProtect: under ADR-012 each version
// is active (or not) on its own. An expired, unreferenced v1.0.0 is
// GC-eligible even while v2.0.0 of the same package is active.
func TestPlanGCOtherVersionActiveDoesNotProtect(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "multi/pkg", "v1.0.0", testNow.Add(-1000*time.Hour))
	store.active[activeKey(domain.EcosystemGo, "multi/pkg", "v2.0.0", testBackend)] = domain.Generation{
		Dependency: domain.DependencyVersion{Version: "v2.0.0"},
	}

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Version != "v1.0.0" {
		t.Errorf("candidates = %+v, want exactly v1.0.0", candidates)
	}
}

// TestPlanGCUnreferencedActiveVersionIsEligible is ADR-012's lifetime
// rule, found live in epic 66's real-container run: references alone
// decide a version's lifetime. With per-version pointers an unreferenced
// version stays active until GC retires it, so "active means never
// eligible" made every unreferenced version immortal.
func TestPlanGCUnreferencedActiveVersionIsEligible(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "active/pkg", "v2.0.0", testNow.Add(-1000*time.Hour))
	store.active[activeKey(domain.EcosystemGo, "active/pkg", "v2.0.0", testBackend)] = domain.Generation{
		Dependency: domain.DependencyVersion{Version: "v2.0.0"},
	}

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Package != "active/pkg" {
		t.Errorf("candidates = %+v, want the unreferenced, grace-expired active version", candidates)
	}
}

// TestPlanGCReferencedActiveVersionIsKept: an active version a project
// still references is never eligible, whatever its grace state.
func TestPlanGCReferencedActiveVersionIsKept(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "active/pkg", "v2.0.0", testNow.Add(-1000*time.Hour))
	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "active/pkg", Version: "v2.0.0", Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	store.active[activeKey(domain.EcosystemGo, "active/pkg", "v2.0.0", testBackend)] = domain.Generation{
		Dependency: domain.DependencyVersion{Version: "v2.0.0"},
	}

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none: a project still references this version", candidates)
	}
}

func TestPlanGCGraceNotYetExpiredIsNotEligible(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "recent/pkg", "v1.0.0", testNow.Add(-1*time.Hour)) // grace just started

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	for _, c := range candidates {
		if c.Package == "recent/pkg" {
			t.Errorf("version with unexpired grace reported as GC-eligible: %+v", c)
		}
	}
}

func TestPlanGCVersionWithProjectReferenceIsNotEligible(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	if err := store.AddReference(ctx, domain.VersionReference{
		ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "used/pkg", Version: "v1.0.0", Reason: domain.ReferenceReasonProject,
	}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	candidates, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none (no grace reference exists, and it's still referenced)", candidates)
	}
}

func TestPlanGCDeterministicGivenSameState(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	addGraceRef(t, store, domain.EcosystemGo, "old/pkg", "v0.1.0", testNow.Add(-400*time.Hour))

	first, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC (first): %v", err)
	}
	second, err := PlanGC(ctx, store, testBackend, 0, testNow)
	if err != nil {
		t.Fatalf("PlanGC (second): %v", err)
	}
	if len(first) != len(second) || len(first) != 1 {
		t.Fatalf("PlanGC not deterministic: first=%+v second=%+v", first, second)
	}
}

// fakeStore is a minimal in-memory ControlStore for RET-003's tests —
// PlanGC's real dependency is *bbolt.Store, already covered by
// references_test.go; this fake isolates PlanGC's own eligibility logic
// (including the active-generation check, which references_test.go's
// fixtures don't set up) without needing a real bbolt file per test.
type fakeStore struct {
	refs   map[string]domain.VersionReference // key: eco|pkg|version|projectID|reason
	active map[string]domain.Generation       // key: eco|pkg|version|backendName (ADR-012)
	gens   []domain.Generation                // GC-001's PlanOrphanGC tests populate this
}

func newFakeStore() *fakeStore {
	return &fakeStore{refs: map[string]domain.VersionReference{}, active: map[string]domain.Generation{}}
}

func refKey(r domain.VersionReference) string {
	return string(r.Ecosystem) + "|" + r.Package + "|" + r.Version + "|" + r.ProjectID + "|" + string(r.Reason)
}

func activeKey(eco domain.Ecosystem, pkg, version, backendName string) string {
	return string(eco) + "|" + pkg + "|" + version + "|" + backendName
}

func (s *fakeStore) AddReference(ctx context.Context, r domain.VersionReference) error {
	s.refs[refKey(r)] = r
	return nil
}

func (s *fakeStore) RemoveReference(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, projectID string) error {
	for k, r := range s.refs {
		if r.Ecosystem == ecosystem && r.Package == pkg && r.Version == version && r.ProjectID == projectID {
			delete(s.refs, k)
		}
	}
	return nil
}

func (s *fakeStore) ListReferences(ctx context.Context, ecosystem domain.Ecosystem, pkg, version string) ([]domain.VersionReference, error) {
	var out []domain.VersionReference
	for _, r := range s.refs {
		if r.Ecosystem == ecosystem && r.Package == pkg && r.Version == version {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) ListAllReferences(ctx context.Context) ([]domain.VersionReference, error) {
	var out []domain.VersionReference
	for _, r := range s.refs {
		out = append(out, r)
	}
	return out, nil
}

func (s *fakeStore) GetActiveGeneration(ctx context.Context, ecosystem domain.Ecosystem, pkg, version, backendName string) (domain.Generation, error) {
	g, ok := s.active[activeKey(ecosystem, pkg, version, backendName)]
	if !ok {
		return domain.Generation{}, errNotFound
	}
	return g, nil
}

func (s *fakeStore) ListAllGenerations(ctx context.Context) ([]domain.Generation, error) {
	return s.gens, nil
}

var errNotFound = &notFoundError{}

type notFoundError struct{}

func (*notFoundError) Error() string { return "not found" }

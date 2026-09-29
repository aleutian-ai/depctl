package retention

import (
	"context"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

// TestPlanSupersededDuplicateGCFindsSameVersionSibling is POINT-004's
// planner regression: two generations for the identical
// (ecosystem, package, version), one ACTIVE and one SUPERSEDED — the
// direct product of the check-then-create race — must flag the
// SUPERSEDED one.
func TestPlanSupersededDuplicateGCFindsSameVersionSibling(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{
		testGen("gen_active", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenActive, now),
		testGen("gen_dup", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now),
	}

	candidates, err := PlanSupersededDuplicateGC(context.Background(), store)
	if err != nil {
		t.Fatalf("PlanSupersededDuplicateGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].GenerationID != "gen_dup" {
		t.Errorf("candidates = %+v, want exactly one candidate for gen_dup", candidates)
	}
}

// TestPlanSupersededDuplicateGCExcludesOlderVersionSuperseded is the
// critical negative case: a SUPERSEDED generation for an OLDER version
// that a NEWER version has since superseded (the ordinary, legitimate
// promotion-chain case, not a duplicate) must never be selected — its
// content is genuinely different from whatever is now ACTIVE, and
// internal/query/search.go's promoted-version check relies on it
// staying present so that older version stays queryable (VALID-002).
func TestPlanSupersededDuplicateGCExcludesOlderVersionSuperseded(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{
		testGen("gen_old", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now.Add(-time.Hour)),
		testGen("gen_new", domain.EcosystemGo, "example.com/foo", "v2.0.0", domain.GenActive, now),
	}

	candidates, err := PlanSupersededDuplicateGC(context.Background(), store)
	if err != nil {
		t.Fatalf("PlanSupersededDuplicateGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none — gen_old is a different (older) version than the currently active one, not a same-version duplicate", candidates)
	}
}

// TestPlanSupersededDuplicateGCExcludesSupersededWithNoActiveSibling
// covers a version that was once promoted but has since been fully
// abandoned (no generation for that exact tuple is ACTIVE anymore,
// e.g. every project dropped it and ordinary grace-period GC will
// eventually collect it) — not this mechanism's job.
func TestPlanSupersededDuplicateGCExcludesSupersededWithNoActiveSibling(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{
		testGen("gen_abandoned", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now),
	}

	candidates, err := PlanSupersededDuplicateGC(context.Background(), store)
	if err != nil {
		t.Fatalf("PlanSupersededDuplicateGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none — no ACTIVE generation shares this tuple, so it's not a proven duplicate", candidates)
	}
}

// TestPlanSupersededDuplicateGCFindsMultipleDuplicateSiblings mirrors
// the real full-scale finding: one exact version can accumulate several
// redundant generations from repeated races, not just one.
func TestPlanSupersededDuplicateGCFindsMultipleDuplicateSiblings(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{
		testGen("gen_active", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenActive, now),
		testGen("gen_dup1", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now),
		testGen("gen_dup2", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now),
		testGen("gen_dup3", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now),
	}

	candidates, err := PlanSupersededDuplicateGC(context.Background(), store)
	if err != nil {
		t.Fatalf("PlanSupersededDuplicateGC: %v", err)
	}
	if len(candidates) != 3 {
		t.Errorf("candidates = %+v, want exactly 3 (gen_dup1/2/3, not gen_active)", candidates)
	}
	for _, c := range candidates {
		if c.GenerationID == "gen_active" {
			t.Errorf("candidates included the ACTIVE generation itself: %+v", candidates)
		}
	}
}

// TestPlanSupersededDuplicateGCIgnoresUnrelatedDependencies confirms the
// tuple grouping is scoped correctly — a duplicate for one dependency
// must never leak into another's candidate set.
func TestPlanSupersededDuplicateGCIgnoresUnrelatedDependencies(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{
		testGen("gen_a_active", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenActive, now),
		testGen("gen_a_dup", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenSuperseded, now),
		testGen("gen_b_active", domain.EcosystemGo, "example.com/bar", "v1.0.0", domain.GenActive, now),
	}

	candidates, err := PlanSupersededDuplicateGC(context.Background(), store)
	if err != nil {
		t.Fatalf("PlanSupersededDuplicateGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].GenerationID != "gen_a_dup" {
		t.Errorf("candidates = %+v, want exactly one candidate for gen_a_dup", candidates)
	}
}

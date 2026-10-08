package retention

import (
	"context"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func testGen(id string, eco domain.Ecosystem, pkg, version string, state domain.GenerationState, updatedAt time.Time) domain.Generation {
	return domain.Generation{
		ID:         id,
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: eco, Name: pkg}, Version: version},
		State:      state,
		UpdatedAt:  updatedAt,
	}
}

func TestPlanOrphanGCIncludesFailedGeneration(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{testGen("gen_1", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenFailed, now)}

	candidates, err := PlanOrphanGC(context.Background(), store, "qdrant", time.Hour, now)
	if err != nil {
		t.Fatalf("PlanOrphanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].GenerationID != "gen_1" || candidates[0].Reason != "failed" {
		t.Errorf("candidates = %+v, want one FAILED candidate for gen_1", candidates)
	}
}

func TestPlanOrphanGCIncludesStaleNonTerminal(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{testGen("gen_1", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenAcquiring, now.Add(-2*time.Hour))}

	candidates, err := PlanOrphanGC(context.Background(), store, "qdrant", time.Hour, now)
	if err != nil {
		t.Fatalf("PlanOrphanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Reason != "stale_nonterminal" {
		t.Errorf("candidates = %+v, want one stale_nonterminal candidate", candidates)
	}
}

func TestPlanOrphanGCExcludesRecentNonTerminal(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	store.gens = []domain.Generation{testGen("gen_1", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenAcquiring, now.Add(-10*time.Minute))}

	candidates, err := PlanOrphanGC(context.Background(), store, "qdrant", time.Hour, now)
	if err != nil {
		t.Fatalf("PlanOrphanGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none — still plausibly in progress", candidates)
	}
}

func TestPlanOrphanGCExcludesFailedButStillActive(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	gen := testGen("gen_1", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenFailed, now)
	store.gens = []domain.Generation{gen}
	store.active[activeKey(domain.EcosystemGo, "example.com/foo", "v1.0.0", "qdrant")] = gen // inconsistent-but-real state

	candidates, err := PlanOrphanGC(context.Background(), store, "qdrant", time.Hour, now)
	if err != nil {
		t.Fatalf("PlanOrphanGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none — still the active pointer, never eligible regardless of state", candidates)
	}
}

func TestPlanOrphanGCExcludesTerminalNonFailedStates(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	states := []domain.GenerationState{
		domain.GenReady, domain.GenActive, domain.GenSuperseded,
		domain.GenGCEligible, domain.GenDeleted, domain.GenDiscovered, domain.GenPlanned,
	}
	for i, s := range states {
		store.gens = append(store.gens, testGen("gen_"+string(rune('a'+i)), domain.EcosystemGo, "example.com/foo", "v1.0.0", s, old))
	}

	candidates, err := PlanOrphanGC(context.Background(), store, "qdrant", time.Hour, now)
	if err != nil {
		t.Fatalf("PlanOrphanGC: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none of these terminal-non-FAILED states ever included", candidates)
	}
}

func TestPlanOrphanGCIsGenerationScopedNotDependencyScoped(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	failed := testGen("gen_failed", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenFailed, now)
	active := testGen("gen_active", domain.EcosystemGo, "example.com/foo", "v1.0.0", domain.GenActive, now)
	store.gens = []domain.Generation{failed, active}
	store.active[activeKey(domain.EcosystemGo, "example.com/foo", "v1.0.0", "qdrant")] = active

	candidates, err := PlanOrphanGC(context.Background(), store, "qdrant", time.Hour, now)
	if err != nil {
		t.Fatalf("PlanOrphanGC: %v", err)
	}
	if len(candidates) != 1 || candidates[0].GenerationID != "gen_failed" {
		t.Errorf("candidates = %+v, want only gen_failed — same dependency+version, different generation IDs, one active one not", candidates)
	}
}

func TestEffectiveOrphanAgeDefaultsWhenNonPositive(t *testing.T) {
	if got := EffectiveOrphanAge(0); got != 24*time.Hour {
		t.Errorf("EffectiveOrphanAge(0) = %v, want 24h default", got)
	}
	if got := EffectiveOrphanAge(-time.Minute); got != 24*time.Hour {
		t.Errorf("EffectiveOrphanAge(-1m) = %v, want 24h default", got)
	}
	if got := EffectiveOrphanAge(2 * time.Hour); got != 2*time.Hour {
		t.Errorf("EffectiveOrphanAge(2h) = %v, want 2h unchanged", got)
	}
}

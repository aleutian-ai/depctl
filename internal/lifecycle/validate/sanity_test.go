package validate

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
)

func TestSanityAutoPassesWithNoPrior(t *testing.T) {
	candidate := domain.Generation{ID: "gen_1"}
	manifest := generation.Manifest{ObjectCount: 5, ChunkCount: 5}

	result, err := Sanity(context.Background(), candidate, nil, manifest, nil, DefaultSanityConfig())
	if err != nil {
		t.Fatalf("Sanity: %v", err)
	}
	if !result.Passed {
		t.Errorf("Sanity.Passed = false, want true (no prior to compare)")
	}
}

func TestSanityFailsOnObjectCountBelowFloor(t *testing.T) {
	prior := domain.Generation{ID: "gen_prior"}
	priorManifest := generation.Manifest{ObjectCount: 1000, ChunkCount: 1000}
	candidate := domain.Generation{ID: "gen_candidate"}
	candidateManifest := generation.Manifest{ObjectCount: 100, ChunkCount: 100} // 10% of prior, below default 50% floor

	result, err := Sanity(context.Background(), candidate, &prior, candidateManifest, &priorManifest, DefaultSanityConfig())
	if err != nil {
		t.Fatalf("Sanity: %v", err)
	}
	if result.Passed {
		t.Fatal("Sanity.Passed = true, want false (object count collapsed below floor)")
	}
}

func TestSanityFailsOnChunkCountAboveCeiling(t *testing.T) {
	prior := domain.Generation{ID: "gen_prior"}
	priorManifest := generation.Manifest{ObjectCount: 100, ChunkCount: 100}
	candidate := domain.Generation{ID: "gen_candidate"}
	candidateManifest := generation.Manifest{ObjectCount: 100, ChunkCount: 500} // 5x prior, above default 3x ceiling

	result, err := Sanity(context.Background(), candidate, &prior, candidateManifest, &priorManifest, DefaultSanityConfig())
	if err != nil {
		t.Fatalf("Sanity: %v", err)
	}
	if result.Passed {
		t.Fatal("Sanity.Passed = true, want false (chunk count spiked above ceiling)")
	}
}

func TestSanityPassesWithinThresholds(t *testing.T) {
	prior := domain.Generation{ID: "gen_prior"}
	priorManifest := generation.Manifest{ObjectCount: 1000, ChunkCount: 1000}
	candidate := domain.Generation{ID: "gen_candidate"}
	candidateManifest := generation.Manifest{ObjectCount: 950, ChunkCount: 1100}

	result, err := Sanity(context.Background(), candidate, &prior, candidateManifest, &priorManifest, DefaultSanityConfig())
	if err != nil {
		t.Fatalf("Sanity: %v", err)
	}
	if !result.Passed {
		t.Errorf("Sanity.Passed = false, want true, failures = %v", result.Failures)
	}
}

func TestSanityThresholdsAreConfigurable(t *testing.T) {
	prior := domain.Generation{ID: "gen_prior"}
	priorManifest := generation.Manifest{ObjectCount: 1000, ChunkCount: 1000}
	candidate := domain.Generation{ID: "gen_candidate"}
	candidateManifest := generation.Manifest{ObjectCount: 600, ChunkCount: 1000} // 60% of prior

	// Default floor (0.5) allows a 60% ratio through.
	result, err := Sanity(context.Background(), candidate, &prior, candidateManifest, &priorManifest, DefaultSanityConfig())
	if err != nil {
		t.Fatalf("Sanity (default config): %v", err)
	}
	if !result.Passed {
		t.Errorf("Sanity.Passed = false with default config, want true, failures = %v", result.Failures)
	}

	// A stricter configured floor (0.8) rejects the same candidate.
	strict := SanityConfig{MinObjectCountRatio: 0.8, MaxChunkCountRatio: 3.0, MaxParserErrorRate: 0.05}
	result, err = Sanity(context.Background(), candidate, &prior, candidateManifest, &priorManifest, strict)
	if err != nil {
		t.Fatalf("Sanity (strict config): %v", err)
	}
	if result.Passed {
		t.Error("Sanity.Passed = true with a stricter configured floor, want false")
	}
}

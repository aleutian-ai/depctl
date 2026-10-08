package validate

import (
	"context"
	"fmt"

	"github.com/aleutian-ai/depctl/internal/data/generation"
	"github.com/aleutian-ai/depctl/internal/domain"
)

// SanityConfig holds the three configurable guardrail thresholds — no
// generic statistical anomaly detector, per VAL-002's simplicity
// constraints.
type SanityConfig struct {
	MinObjectCountRatio float64 // candidate must have >= this fraction of prior's object count
	MaxChunkCountRatio  float64 // candidate must have <= this multiple of prior's chunk count
	MaxParserErrorRate  float64 // candidate's parser error rate must be <= this
}

// DefaultSanityConfig matches the values in the design spec's example
// config.
func DefaultSanityConfig() SanityConfig {
	return SanityConfig{MinObjectCountRatio: 0.5, MaxChunkCountRatio: 3.0, MaxParserErrorRate: 0.05}
}

// Sanity compares candidate's object/chunk counts against prior (sync
// passes the dependency's most recently promoted generation of any other
// version, if any) and blocks
// promotion if the candidate looks implausible — e.g. an object count
// collapse from 18,000 to 220 without genuine cause. If prior is nil
// (first sync for this dependency), there's nothing to compare against
// and the check auto-passes.
//
// MaxParserErrorRate is always satisfied under the current
// generation.Build implementation: GEN-002 deliberately fails the whole
// generation on any single normalizer error rather than tolerating a
// per-file failure rate (see that ticket's Tests section), so a
// generation that reached VALIDATING never has a nonzero parse error
// rate to report. The threshold is still exposed here — and checked, at
// a rate that's always 0 — so config parity holds if a future version of
// Build starts tolerating partial failures.
func Sanity(ctx context.Context, candidate domain.Generation, prior *domain.Generation, candidateManifest generation.Manifest, priorManifest *generation.Manifest, cfg SanityConfig) (StructuralResult, error) {
	if prior == nil || priorManifest == nil {
		return StructuralResult{Passed: true}, nil
	}

	var failures []string

	if priorManifest.ObjectCount > 0 {
		ratio := float64(candidateManifest.ObjectCount) / float64(priorManifest.ObjectCount)
		if ratio < cfg.MinObjectCountRatio {
			failures = append(failures, fmt.Sprintf("object count ratio %.2f is below floor %.2f (candidate=%d, prior=%d)", ratio, cfg.MinObjectCountRatio, candidateManifest.ObjectCount, priorManifest.ObjectCount))
		}
	}

	if priorManifest.ChunkCount > 0 {
		ratio := float64(candidateManifest.ChunkCount) / float64(priorManifest.ChunkCount)
		if ratio > cfg.MaxChunkCountRatio {
			failures = append(failures, fmt.Sprintf("chunk count ratio %.2f exceeds ceiling %.2f (candidate=%d, prior=%d)", ratio, cfg.MaxChunkCountRatio, candidateManifest.ChunkCount, priorManifest.ChunkCount))
		}
	}

	const parserErrorRate = 0.0 // see doc comment above
	if parserErrorRate > cfg.MaxParserErrorRate {
		failures = append(failures, fmt.Sprintf("parser error rate %.2f exceeds ceiling %.2f", parserErrorRate, cfg.MaxParserErrorRate))
	}

	return StructuralResult{Passed: len(failures) == 0, Failures: failures}, nil
}

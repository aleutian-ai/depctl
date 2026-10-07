// Package validate implements ragctl's deterministic pre-promotion
// checks (VAL-001..003): structural completeness, sanity thresholds
// against the prior active generation, and a version-correctness smoke
// test against the live search index. Everything here is deterministic
// and testable without an LLM, per the project's core development
// principle — no semantic/relevance scoring belongs in this package.
package validate

import (
	"context"
	"fmt"

	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
)

// StructuralResult is the shared pass/fail-with-reasons shape every
// check in this package returns, so the promotion flow (VAL-004) can
// handle them uniformly.
type StructuralResult struct {
	Passed   bool
	Failures []string
}

// Structural runs deterministic structural checks against a candidate
// generation: non-zero counts, backend point count matching the
// manifest, manifest/generation identity, and Badger's own chunk count
// matching what the manifest claims. Failures are collected, not
// fail-fast, so one run reports everything wrong at once.
//
// Deviates from the original sketch in two ways, both because the
// fields it assumed don't exist in ragctl's actual manifest/generation
// shapes: "manifest.Hash != """ becomes "manifest.ID == gen.ID" (the
// generation package's Manifest has no content-hash field, so this
// checks the more directly useful invariant — that the manifest
// actually belongs to this generation); "every chunk's version metadata
// equals gen.DependencyVersion.Version" is dropped in favor of "Badger's
// actual chunk count matches the manifest's claimed count" — per-chunk
// version metadata only exists once chunks are replicated into a vector
// backend (Badger's domain.Chunk carries no version field), which is
// exactly what VersionCorrectness (VAL-003) already checks live; doing
// it twice here would be redundant, and a Badger-only substitute would
// have to reach into each chunk's parent KnowledgeObject, which — after
// GEN-003 content reuse — reflects whichever generation first created
// that object, not necessarily this one, so it would false-positive on
// legitimate reuse.
func Structural(ctx context.Context, gen domain.Generation, manifest generation.Manifest, replica domain.BackendReplica, badgerStore *badger.Store) (StructuralResult, error) {
	var failures []string

	if len(manifest.Sources) == 0 {
		failures = append(failures, "source count is zero")
	}
	if manifest.ObjectCount == 0 {
		failures = append(failures, "object count is zero")
	}
	if manifest.ChunkCount == 0 {
		failures = append(failures, "chunk count is zero")
	}
	if replica.PointCount != manifest.ChunkCount {
		failures = append(failures, fmt.Sprintf("backend point count (%d) does not match manifest chunk count (%d)", replica.PointCount, manifest.ChunkCount))
	}
	if manifest.ID != gen.ID {
		failures = append(failures, fmt.Sprintf("manifest ID (%s) does not match generation ID (%s)", manifest.ID, gen.ID))
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		return StructuralResult{}, fmt.Errorf("validate: list chunks for %s: %w", gen.ID, err)
	}
	if len(chunks) != manifest.ChunkCount {
		failures = append(failures, fmt.Sprintf("actual staged chunk count (%d) does not match manifest chunk count (%d)", len(chunks), manifest.ChunkCount))
	}

	return StructuralResult{Passed: len(failures) == 0, Failures: failures}, nil
}

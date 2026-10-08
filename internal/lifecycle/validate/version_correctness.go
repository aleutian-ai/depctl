package validate

import (
	"context"
	"fmt"
	"sort"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/data/generation"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/embedding"
)

// sampleSize is the fixed number of chunks sampled from the candidate
// generation to use as smoke-test queries — no separate curated query
// fixture set needed for this check.
const sampleSize = 5

// SampleChunks deterministically selects up to sampleSize chunks from
// all of a generation's staged chunks, sorted by chunk ID so results are
// reproducible across runs and test environments.
func SampleChunks(chunks []domain.Chunk) []domain.Chunk {
	sorted := make([]domain.Chunk, len(chunks))
	copy(sorted, chunks)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	if len(sorted) > sampleSize {
		sorted = sorted[:sampleSize]
	}
	return sorted
}

// VersionCorrectness embeds each of sampleChunks' own text, queries vb
// (filtered to gen.ID) for matches, and asserts every returned point's
// version/generation metadata equals gen's — a deterministic proof the
// replica isn't cross-contaminated with another version's data. An empty
// result for a chunk that was just embedded from the generation's own
// content is itself a failure: it means the replica is missing content
// it should have. With a nil embedder (keyword-only search, LOCAL-001)
// each chunk's own text is the query instead.
func VersionCorrectness(ctx context.Context, embedder *embedding.Prompted, vb backend.VectorBackend, ns backend.Namespace, gen domain.Generation, sampleChunks []domain.Chunk) (StructuralResult, error) {
	if len(sampleChunks) == 0 {
		return StructuralResult{Passed: true}, nil
	}

	texts := make([]string, len(sampleChunks))
	for i, c := range sampleChunks {
		texts[i] = string(c.Content)
	}
	vectors := make([][]float32, len(sampleChunks))
	if embedder != nil {
		var err error
		docs := make([]embedding.Document, len(sampleChunks))
		for i, c := range sampleChunks {
			docs[i] = embedding.Document{Title: generation.ChunkTitle(c), Text: string(c.Content)}
		}
		vectors, err = embedder.EmbedDocuments(ctx, docs)
		if err != nil {
			return StructuralResult{}, fmt.Errorf("validate: embed sample chunks: %w", err)
		}
		if len(vectors) != len(sampleChunks) {
			return StructuralResult{}, fmt.Errorf("validate: embedder returned %d vectors for %d sample chunks", len(vectors), len(sampleChunks))
		}
	}

	var failures []string
	wantVersion := gen.Dependency.Version

	for i, c := range sampleChunks {
		result, err := vb.Query(ctx, backend.QueryRequest{
			Namespace: ns.Name,
			Vector:    vectors[i],
			Text:      texts[i],
			TopK:      10,
			Filter:    &backend.Filter{Generation: gen.ID},
		})
		if err != nil {
			return StructuralResult{}, fmt.Errorf("validate: query for chunk %s: %w", c.ID, err)
		}
		if len(result.Points) == 0 {
			failures = append(failures, fmt.Sprintf("chunk %s: query returned no results within generation %s", c.ID, gen.ID))
			continue
		}
		for _, p := range result.Points {
			if p.Metadata.Version != wantVersion {
				failures = append(failures, fmt.Sprintf("chunk %s query returned point %s from version %q, want %q", c.ID, p.ID, p.Metadata.Version, wantVersion))
			}
			if p.Metadata.Generation != gen.ID {
				failures = append(failures, fmt.Sprintf("chunk %s query returned point %s from generation %q, want %q", c.ID, p.ID, p.Metadata.Generation, gen.ID))
			}
		}
	}

	return StructuralResult{Passed: len(failures) == 0, Failures: failures}, nil
}

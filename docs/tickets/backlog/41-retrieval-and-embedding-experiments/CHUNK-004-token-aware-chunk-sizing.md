# CHUNK-004: Token-aware chunk sizing

**Epic:** Retrieval and Embedding Experiments
**Status:** planned
**Depends on:** CHUNK-002 (`internal/data/chunk/markdown.Chunker`)
**Estimated size:** small

## Goal
Replace the byte-length-only cap in `internal/data/chunk/markdown.Chunker` with a pluggable size estimator, so chunk sizing can move toward token-awareness without pulling in a real tokenizer dependency yet. Default behavior stays a cheap heuristic (`len(s)/4`), not raw byte length — close enough to token count for the packing decision to matter, per the scratch doc's §8.2.

## Non-goals
- No real tokenizer (tiktoken, sentencepiece, etc.) dependency — the scratch doc is explicit: "Do not add heavyweight tokenizer dependencies until an actual model-specific limit requires them."
- No change to section-splitting logic (`splitSections`/heading-boundary detection in `internal/data/chunk/markdown/sections.go`) — this ticket only changes how a section's size is measured against the configured limit, not how sections/paragraphs are identified.
- No per-embedding-model-specific estimator selection wiring (e.g. picking a different `TokenEstimator` based on `config.EmbeddingConfig.Model`) — that's a natural follow-up once a real tokenizer is justified, out of scope here.

## Simplicity constraints
- `TokenEstimator` is a plain `func(string) int` — no interface, no estimator registry, matching the scratch doc's own sketch verbatim.
- The default estimator is a one-line heuristic (`len(s) / 4`, floor at 1 for non-empty input) — not a real tokenizer, not even a whitespace/word-count approximation; simplicity over accuracy until evidence says otherwise.
- `MaxChunkBytes`/`DefaultMaxChunkBytes` naming stays as-is where it still means bytes (e.g. any remaining hard byte ceiling as a safety backstop); the new token-based limit is an additional, separately-named field, not a silent reinterpretation of what "MaxChunkBytes" means.

## Design
Extend `internal/data/chunk/markdown/markdown.go`:

```go
// TokenEstimator estimates how many tokens a string will consume once
// embedded/sent to a model — a cheap approximation, not a real
// tokenizer count (see DefaultTokenEstimator).
type TokenEstimator func(string) int

// DefaultTokenEstimator approximates token count as len(s)/4, a common
// rough-order-of-magnitude heuristic for English prose/code — good
// enough for a packing decision, not for exact token accounting.
func DefaultTokenEstimator(s string) int {
    if len(s) == 0 {
        return 0
    }
    if n := len(s) / 4; n > 0 {
        return n
    }
    return 1
}

// Chunker implements chunk.Chunker for Markdown/plain-text
// KnowledgeObjects (ContentType "markdown" or "text").
type Chunker struct {
    maxChunkBytes  int // hard byte-length backstop; DefaultMaxChunkBytes if <= 0
    maxTokens      int // 0 disables token-based splitting, byte cap alone governs
    tokenEstimator TokenEstimator
}

// Option configures a Chunker.
type Option func(*Chunker)

// WithTokenLimit bounds a chunk by estimated token count (via
// estimator, or DefaultTokenEstimator if estimator is nil) in addition
// to the byte-length backstop. maxTokens <= 0 disables token-based
// splitting (today's byte-only behavior).
func WithTokenLimit(maxTokens int, estimator TokenEstimator) Option

// New returns a Chunker bounding chunks to maxChunkBytes (byte
// backstop; a non-positive value falls back to DefaultMaxChunkBytes),
// with any Options applied on top.
func New(maxChunkBytes int, opts ...Option) *Chunker
```
`packSection` (in `sections.go`) takes the size-check as a small function value rather than a hardcoded `len(part) > maxChunkBytes` comparison, so it can check both the byte backstop and (when configured) the token estimate:
```go
func fits(part string, maxBytes int, maxTokens int, estimate TokenEstimator) bool {
    if len(part) > maxBytes {
        return false
    }
    if maxTokens > 0 && estimate(part) > maxTokens {
        return false
    }
    return true
}
```
Calling `New(maxChunkBytes)` with no `Option`s preserves exactly today's byte-only behavior (`maxTokens == 0`), so every existing caller/test is unaffected until it opts in.

## Inputs / Outputs
- Input: unchanged — `Chunk(ctx, obj)`.
- Output: unchanged shape (`[]domain.Chunk`) — when `WithTokenLimit` is unused, chunk boundaries are byte-for-byte identical to today's output (verified by a regression test against existing fixtures).
- When `WithTokenLimit(n, estimator)` is set, a section is further split at paragraph boundaries (same splitting mechanism `packSection` already uses) whenever either the byte backstop or the estimated-token limit is exceeded — whichever triggers first.

## Failure behavior
- `estimator` is `nil` when passed to `WithTokenLimit` → `DefaultTokenEstimator` is used silently (documented default), not an error — matching `New`'s own "non-positive falls back to default" precedent.
- An estimator that panics on some input is not specially guarded against — same trust boundary as any other caller-supplied function value in this codebase; not treated as a failure mode this ticket needs to handle.

## Tests
- `New(maxChunkBytes)` with no options produces identical output to the current `Chunker` on the existing fixture set (`internal/data/chunk/markdown/testdata`) — a straight regression check.
- `WithTokenLimit` with a small `maxTokens` and `DefaultTokenEstimator` splits a section that fits under the byte backstop but exceeds the token estimate.
- A custom estimator (e.g. word-count-based) is honored instead of the default when explicitly passed.
- `WithTokenLimit(0, nil)` behaves identically to not calling it at all (explicit disable is a no-op, not an error).

## Acceptance criteria
- [ ] `TokenEstimator` type and `DefaultTokenEstimator` added, matching the scratch doc's sketch.
- [ ] `WithTokenLimit` option added; unset preserves today's byte-only chunking exactly.
- [ ] No tokenizer dependency added to `go.mod`.
- [ ] Existing `internal/data/chunk/markdown` tests pass unmodified against the default (no-token-limit) path.

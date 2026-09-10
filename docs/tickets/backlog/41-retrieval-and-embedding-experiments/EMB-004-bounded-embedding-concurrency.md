# EMB-004: Bounded embedding concurrency

**Epic:** Retrieval and Embedding Experiments
**Status:** planned
**Depends on:** EMB-002 (`internal/embedding/ollama.Client`)
**Estimated size:** small

## Goal
Add a `WithConcurrency(n int)` functional option to `internal/embedding/ollama.Client`, following the same option pattern the real `WithBatchSize`/`WithHTTPClient` already use, so independent `/api/embed` batches can be sent concurrently — default `n=1` preserves today's fully sequential behavior exactly.

## Non-goals
- No change to `Embed`'s per-batch behavior (`c.batchSize` texts per request) — concurrency is across batches, not within one.
- No embedding-provider-agnostic concurrency abstraction in `internal/embedding.Embedder` — this ticket is scoped to the `ollama` implementation only, matching the ticket's own simplicity constraint of touching one package.
- No dynamic/adaptive concurrency (backpressure-tuned worker count) — a fixed `n` set at construction time, nothing more.

## Simplicity constraints
- A bounded worker pool (`n` goroutines pulling batch indices off a channel, or a simple semaphore-gated `sync.WaitGroup` loop) — no generic worker-pool package dependency.
- Result ordering must be deterministic regardless of which goroutine finishes first: write each batch's result into an indexed `[][]float32` slot by its batch index, not by completion order.
- Error semantics stay deterministic: if multiple batches fail concurrently, `Embed` returns the error from the lowest-indexed failing batch (same "first failure wins, in input order" semantics a sequential loop would have produced), not whichever goroutine happened to error first.

## Design
Extend `internal/embedding/ollama/ollama.go`:

```go
// Client implements embedding.Embedder against Ollama's /api/embed
// endpoint via a small hand-written HTTP client — no generated SDK, no
// third-party Ollama client dependency.
type Client struct {
    endpoint    string
    model       string
    httpClient  *http.Client
    batchSize   int
    concurrency int // number of in-flight batch requests; 1 = sequential (default)

    dimsOnce sync.Once
    dims     int
    dimsErr  error
}

// WithConcurrency bounds how many /api/embed batch requests may be
// in flight at once. n <= 1 preserves today's sequential behavior
// (the default).
func WithConcurrency(n int) Option {
    return func(c *Client) { c.concurrency = n }
}
```

`Embed` changes from a sequential `for` loop to a bounded-fan-out version when `c.concurrency > 1`:
```go
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    if len(texts) == 0 {
        return nil, nil
    }

    batches := splitBatches(texts, c.batchSize) // []( []string ), same slicing Embed already does
    results := make([][][]float32, len(batches))
    errs := make([]error, len(batches))

    sem := make(chan struct{}, max(1, c.concurrency))
    var wg sync.WaitGroup
    for i, batch := range batches {
        wg.Add(1)
        sem <- struct{}{}
        go func(i int, batch []string) {
            defer wg.Done()
            defer func() { <-sem }()
            results[i], errs[i] = c.embedBatch(ctx, batch)
        }(i, batch)
    }
    wg.Wait()

    for i, err := range errs {
        if err != nil {
            return nil, err // lowest-indexed failure, deterministic regardless of goroutine completion order
        }
    }

    var out [][]float32
    for _, r := range results {
        out = append(out, r...)
    }
    // existing dims-seeding and per-vector dimension check unchanged, applied to out
    ...
}
```
When `c.concurrency <= 1`, `Embed` keeps today's exact sequential loop (no goroutines spawned at all) — not merely a worker pool sized to 1, so the default path has zero behavioral or performance delta from what ships today.

## Inputs / Outputs
- Input: unchanged — `Embed(ctx, texts)`.
- Output: unchanged shape (`[][]float32`, one vector per input text, same order) — concurrency is an internal implementation detail, invisible at the interface.

## Failure behavior
- Any batch's `embedBatch` returns an error → `Embed` returns that error (deterministically the lowest-indexed failing batch, per Simplicity constraints), same as today's sequential short-circuit.
- Context cancellation mid-flight → in-flight goroutines' `embedBatch` calls observe `ctx.Done()` the same way they already do sequentially (existing retry-loop cancellation check in `embedBatch` is unchanged); `Embed` returns once all outstanding goroutines have returned.

## Tests
- `WithConcurrency` unset (default) or set to `1`/`0` → `Embed` takes the exact same code path as before this ticket (verified by call-order/spy on the underlying `http.RoundTripper`, matching sequential request ordering).
- `WithConcurrency(4)` against an `httptest.Server` with an artificial per-request delay → wall-clock time for N batches drops versus sequential, and returned vectors are in the same order as the input texts regardless of response timing (delay the response for an early batch more than a later one, assert order is still correct).
- Two batches fail concurrently (server returns errors for both) → `Embed` returns the error for the lower-indexed batch, deterministically, across repeated runs.
- Dimension mismatch check still runs against the concatenated, correctly-ordered result.

## Acceptance criteria
- [ ] `WithConcurrency(n)` added, default `n=1`, sequential behavior byte-for-byte unchanged when unset.
- [ ] Batch results are ordered deterministically by input order, never by goroutine completion order.
- [ ] Errors are deterministic (lowest-indexed failing batch) under concurrent failures.
- [ ] No new third-party worker-pool dependency.

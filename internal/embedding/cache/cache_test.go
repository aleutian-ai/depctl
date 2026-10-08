package cache

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/aleutian-ai/depctl/internal/data/badger"
)

// countingEmbedder wraps a fixed vector generator and counts how many
// times Embed was actually called (and with how many texts total), so
// tests can assert on cache hit/miss behavior at the inner embedder.
type countingEmbedder struct {
	name, model string
	dims        int
	calls       int32
	textsSeen   int32
}

func (c *countingEmbedder) Name() string    { return c.name }
func (c *countingEmbedder) ModelID() string { return c.model }
func (c *countingEmbedder) Dimensions(ctx context.Context) (int, error) {
	return c.dims, nil
}
func (c *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	atomic.AddInt32(&c.calls, 1)
	atomic.AddInt32(&c.textsSeen, int32(len(texts)))
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, c.dims)
		for j := range v {
			v[j] = float32(len(t) + j)
		}
		out[i] = v
	}
	return out, nil
}

func openTestStore(t *testing.T) *badger.Store {
	t.Helper()
	s, err := badger.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEmbedSameChunkTwiceCallsInnerOnce(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	inner := &countingEmbedder{name: "fake", model: "m1", dims: 3}
	c := New(inner, store)

	if _, err := c.Embed(ctx, []string{"hello world"}); err != nil {
		t.Fatalf("Embed 1: %v", err)
	}
	if _, err := c.Embed(ctx, []string{"hello world"}); err != nil {
		t.Fatalf("Embed 2: %v", err)
	}

	if got := atomic.LoadInt32(&inner.calls); got != 1 {
		t.Errorf("inner.calls = %d, want 1", got)
	}
	reused, generated := c.Counts()
	if reused != 1 || generated != 1 {
		t.Errorf("Counts = reused=%d generated=%d, want reused=1 generated=1", reused, generated)
	}
}

func TestEmbedReturnsIdenticalVectorOnCacheHit(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	inner := &countingEmbedder{name: "fake", model: "m1", dims: 3}
	c := New(inner, store)

	first, err := c.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed 1: %v", err)
	}
	second, err := c.Embed(ctx, []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed 2: %v", err)
	}
	if len(first[0]) != len(second[0]) {
		t.Fatalf("vector length changed: %d vs %d", len(first[0]), len(second[0]))
	}
	for i := range first[0] {
		if first[0][i] != second[0][i] {
			t.Errorf("vector[%d] = %v, want %v", i, second[0][i], first[0][i])
		}
	}
}

func TestModelChangeIsCacheMiss(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	innerA := &countingEmbedder{name: "fake", model: "m1", dims: 3}
	cA := New(innerA, store)
	if _, err := cA.Embed(ctx, []string{"hello world"}); err != nil {
		t.Fatalf("Embed via m1: %v", err)
	}

	innerB := &countingEmbedder{name: "fake", model: "m2", dims: 3}
	cB := New(innerB, store)
	if _, err := cB.Embed(ctx, []string{"hello world"}); err != nil {
		t.Fatalf("Embed via m2: %v", err)
	}

	if got := atomic.LoadInt32(&innerB.calls); got != 1 {
		t.Errorf("innerB.calls = %d, want 1 (different model must not reuse m1's cache entry)", got)
	}
}

func TestEmbedMixedHitsAndMissesPreservesOrder(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	inner := &countingEmbedder{name: "fake", model: "m1", dims: 2}
	c := New(inner, store)

	if _, err := c.Embed(ctx, []string{"a", "c"}); err != nil {
		t.Fatalf("prime cache: %v", err)
	}

	vecs, err := c.Embed(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}

	reused, generated := c.Counts()
	if reused != 2 || generated != 3 {
		// prime call: 2 generated; second call: "a","c" hit (2 reused), "b" miss (1 generated)
		t.Errorf("Counts = reused=%d generated=%d, want reused=2 generated=3", reused, generated)
	}
	if got := atomic.LoadInt32(&inner.textsSeen); got != 3 {
		t.Errorf("inner.textsSeen = %d, want 3 (a,c primed + b missed)", got)
	}
}

func TestVectorEncodingRoundTripsAndIsCompact(t *testing.T) {
	v := make([]float32, 768)
	for i := range v {
		v[i] = float32(i)*0.001 - 0.3
	}
	data := encodeVector(v)
	if len(data) != 1+4*768 {
		t.Errorf("encoded length = %d, want %d", len(data), 1+4*768)
	}
	got, err := decodeVector(data)
	if err != nil || len(got) != len(v) {
		t.Fatalf("decode: %v (len %d)", err, len(got))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("dimension %d = %v, want %v", i, got[i], v[i])
		}
	}
}

// TestLegacyJSONEntriesStillDecode: caches written before the binary
// format keep working, and a truncated binary entry is an error (a miss),
// never a wrong vector.
func TestLegacyJSONEntriesStillDecode(t *testing.T) {
	got, err := decodeVector([]byte(`{"vector":[1.5,-2,0.25]}`))
	if err != nil || len(got) != 3 || got[0] != 1.5 || got[1] != -2 || got[2] != 0.25 {
		t.Errorf("legacy decode = %v (err %v)", got, err)
	}
	if _, err := decodeVector([]byte{binaryVectorTag, 1, 2, 3}); err == nil {
		t.Error("truncated binary vector decoded without error")
	}
}

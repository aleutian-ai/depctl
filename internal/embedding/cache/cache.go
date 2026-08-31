// Package cache wraps any embedding.Embedder with a Badger-backed,
// content-hash-keyed cache (EMB-003), so unchanged chunks are never
// re-embedded across generations or re-syncs.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	dchunk "aleutian-ai/ragctl/internal/data/chunk"
	"aleutian-ai/ragctl/internal/embedding"

	"aleutian-ai/ragctl/internal/data/badger"
)

// CachingEmbedder implements embedding.Embedder by checking a Badger
// cache before delegating misses to inner. The cache key is
// content-addressed on purpose (chunk content hash + provider + model),
// not scoped to a generation — the same chunk content re-appearing in a
// later generation, or an unrelated one, is still a hit.
type CachingEmbedder struct {
	inner embedding.Embedder
	store *badger.Store

	mu     sync.Mutex
	reused int
	made   int
}

// New wraps inner with a Badger-backed cache.
func New(inner embedding.Embedder, store *badger.Store) *CachingEmbedder {
	return &CachingEmbedder{inner: inner, store: store}
}

// Name delegates to the wrapped Embedder.
func (c *CachingEmbedder) Name() string { return c.inner.Name() }

// ModelID delegates to the wrapped Embedder.
func (c *CachingEmbedder) ModelID() string { return c.inner.ModelID() }

// Dimensions delegates to the wrapped Embedder.
func (c *CachingEmbedder) Dimensions(ctx context.Context) (int, error) {
	return c.inner.Dimensions(ctx)
}

// Counts returns the number of embeddings reused from cache and newly
// generated across every Embed call made on this CachingEmbedder so far.
func (c *CachingEmbedder) Counts() (reused, generated int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reused, c.made
}

// cacheEntry is the JSON value stored per cache key.
type cacheEntry struct {
	Vector []float32 `json:"vector"`
}

// Embed returns one vector per text, in the same order as texts,
// resolving each from cache where possible and calling inner.Embed only
// for the misses.
func (c *CachingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	provider, model := c.inner.Name(), c.inner.ModelID()
	out := make([][]float32, len(texts))
	var missIdx []int
	var missTexts []string

	for i, text := range texts {
		key := cacheKey(text, provider, model)
		data, err := c.store.GetEmbeddingMetadata(ctx, key)
		if err == nil {
			var entry cacheEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				log.Printf("embedding cache: corrupt entry for key %s: %v (treating as miss)", key, err)
			} else {
				out[i] = entry.Vector
				continue
			}
		} else if !errors.Is(err, badger.ErrNotFound) {
			log.Printf("embedding cache: read error for key %s: %v (falling through to inner embedder)", key, err)
		}
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, text)
	}

	if len(missTexts) > 0 {
		vecs, err := c.inner.Embed(ctx, missTexts)
		if err != nil {
			return nil, fmt.Errorf("embedding cache: inner embed: %w", err)
		}
		writeBroken := false
		for j, idx := range missIdx {
			out[idx] = vecs[j]
			if writeBroken {
				continue
			}
			key := cacheKey(texts[idx], provider, model)
			data, err := json.Marshal(cacheEntry{Vector: vecs[j]})
			if err != nil {
				log.Printf("embedding cache: encode entry for key %s: %v", key, err)
				continue
			}
			if err := c.store.PutEmbeddingMetadata(ctx, key, data); err != nil {
				log.Printf("embedding cache: write error for key %s: %v (skipping cache writes for the rest of this batch)", key, err)
				writeBroken = true
			}
		}
	}

	c.mu.Lock()
	c.reused += len(texts) - len(missTexts)
	c.made += len(missTexts)
	c.mu.Unlock()

	return out, nil
}

// cacheKey derives a deterministic key from text's content hash plus
// provider and model, so a model or provider change never reuses a
// stale vector. Passed to badger.Store's existing
// PutEmbeddingMetadata/GetEmbeddingMetadata (STORE-003) under the
// "embedcache/" sub-prefix named in EMB-003's design — a distinct shape
// from the generation-scoped "embed/<generation-id>/..." keys those
// methods also serve, since a generation ID is always a "gen_"-prefixed
// ULID and never literally "embedcache".
func cacheKey(text, provider, model string) string {
	return "embedcache/" + dchunk.ContentHash([]byte(text)) + "/" + provider + "/" + model
}

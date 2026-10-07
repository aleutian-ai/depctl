package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/keyword"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/registry"
)

// readinessSettleTimeout is how long auto mode waits for a readiness probe
// that's still running before deciding without it, so a sync right at
// daemon startup doesn't needlessly build keyword-only.
const readinessSettleTimeout = 10 * time.Second

// searchIndex is every search index an install writes, as one
// VectorBackend (LOCAL-001/002): the keyword index (auto and keyword
// modes) and the vector store (vector mode, and auto mode when the
// embedder is ready). Writes and deletes reach all of them, so sync, GC
// and promotion work unchanged in every mode. Name is the install's
// active-generation key (vector.backend) in every mode, so a generation's
// lifecycle doesn't depend on how it's searched.
type searchIndex struct {
	name    string
	vector  backend.VectorBackend
	keyword backend.VectorBackend
}

// Name is the active-generation key, the same in every retrieval mode.
func (s *searchIndex) Name() string { return s.name }

// Capabilities is the union of the indexes' capabilities.
func (s *searchIndex) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	var out backend.Capabilities
	for _, b := range s.members() {
		c, err := b.Capabilities(ctx)
		if err != nil {
			return out, err
		}
		out.VectorSearch = out.VectorSearch || c.VectorSearch
		out.KeywordSearch = out.KeywordSearch || c.KeywordSearch
		out.HybridSearch = out.HybridSearch || c.HybridSearch
		out.MetadataFilter = out.MetadataFilter || c.MetadataFilter
		out.DeleteByFilter = out.DeleteByFilter || c.DeleteByFilter
	}
	return out, nil
}

// Health checks every index.
func (s *searchIndex) Health(ctx context.Context) error {
	for _, b := range s.members() {
		if err := b.Health(ctx); err != nil {
			return err
		}
	}
	return nil
}

// EnsureNamespace creates ns in every index.
func (s *searchIndex) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	for _, b := range s.members() {
		if err := b.EnsureNamespace(ctx, ns); err != nil {
			return err
		}
	}
	return nil
}

// Upsert writes to every index: the keyword index takes points' text,
// the vector store their vectors.
func (s *searchIndex) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	for _, b := range s.members() {
		if err := b.Upsert(ctx, req); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes from every index.
func (s *searchIndex) Delete(ctx context.Context, req backend.DeleteRequest) error {
	for _, b := range s.members() {
		if err := b.Delete(ctx, req); err != nil {
			return err
		}
	}
	return nil
}

// Query uses the vector store when the request has a vector, and keyword
// search otherwise. A vector search that finds nothing falls back to
// keyword search too: a filtered vector search always returns the
// nearest points that exist, so finding none means the generation was
// built without vectors (while the embedder was unavailable).
func (s *searchIndex) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	if s.vector != nil && req.Vector != nil {
		res, err := s.vector.Query(ctx, req)
		if err != nil || len(res.Points) > 0 || s.keyword == nil {
			return res, err
		}
	}
	if s.keyword == nil {
		return backend.QueryResult{}, fmt.Errorf("retrieval.mode is vector, but no query vector was given (is the embedder ready?)")
	}
	return s.keyword.Query(ctx, req)
}

// Count is the largest count across the indexes: a generation is
// present if any index holds it (auto mode builds keyword-only while the
// embedder is unavailable, and an install switched to auto from vector
// mode starts with an empty keyword index). A namespace an index never
// created counts 0 there (a conformance rule), so any error is real.
func (s *searchIndex) Count(ctx context.Context, ns string, filter *backend.Filter) (int, error) {
	most := 0
	for _, b := range s.members() {
		n, err := b.Count(ctx, ns, filter)
		if err != nil {
			return 0, err
		}
		most = max(most, n)
	}
	return most, nil
}

func (s *searchIndex) members() []backend.VectorBackend {
	var out []backend.VectorBackend
	if s.keyword != nil {
		out = append(out, s.keyword)
	}
	if s.vector != nil {
		out = append(out, s.vector)
	}
	return out
}

// buildSearchIndex returns the indexes this install writes: the keyword
// index unless retrieval.mode is vector, plus the vector store when
// withVectors (never in keyword mode).
func buildSearchIndex(cfg config.Config, withVectors bool) (*searchIndex, error) {
	mode := cfg.Retrieval.ModeOrDefault()
	idx := &searchIndex{name: cfg.Vector.Backend}
	if mode != config.RetrievalVector {
		path, err := keywordIndexPath(cfg)
		if err != nil {
			return nil, err
		}
		idx.keyword = keyword.New(path)
	}
	if withVectors && mode != config.RetrievalKeyword {
		vb, err := buildVectorBackend(cfg)
		if err != nil {
			return nil, err
		}
		idx.vector = vb
	}
	return idx, nil
}

// buildAllIndexes is every index that may hold this install's data, for
// cleanup and health: GC must delete from the vector store in auto mode
// even when the embedder is down.
func buildAllIndexes(cfg config.Config) (*searchIndex, error) {
	return buildSearchIndex(cfg, cfg.Retrieval.ModeOrDefault() != config.RetrievalKeyword)
}

// keywordIndexPath is the keyword index's file: keyword.db next to the
// configured control.db (the data dir, by default).
func keywordIndexPath(cfg config.Config) (string, error) {
	return filepath.Join(filepath.Dir(cfg.Storage.Control.Path), "keyword.db"), nil
}

// usesEmbedder reports whether cfg's retrieval mode can use the embedder
// at all.
func usesEmbedder(cfg config.Config) bool {
	return cfg.Retrieval.ModeOrDefault() != config.RetrievalKeyword
}

// vectorsReady is auto mode's decision for one build or search: use
// vectors only when both the embedder and the vector store are ready.
// Callers outside the daemon pass nil readiness, which counts as ready.
func vectorsReady(r *embeddingReadiness, v *vectorReadiness) bool {
	return r.readyWithin(readinessSettleTimeout) && v.readyWithin(readinessSettleTimeout)
}

// backfillKeyword adds keyword entries for active generations missing
// from the keyword index: ones built while the install was in vector
// mode, before it switched to auto or keyword. No model is involved, so
// this is local and cheap; the per-generation check is a prefix-scoped
// count.
func backfillKeyword(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, reg *registry.Registry, out io.Writer) error {
	path, err := keywordIndexPath(cfg)
	if err != nil {
		return err
	}
	kw := keyword.New(path)
	ns := backend.Namespace{Name: cfg.Vector.Collection}
	pointers, err := store.ListActivePointers(ctx, cfg.Vector.Backend)
	if err != nil {
		return fmt.Errorf("list active generations: %w", err)
	}
	for _, p := range pointers {
		filter := &backend.Filter{Ecosystem: string(p.Ecosystem), Dependency: p.Dependency, Version: p.Version, Generation: p.GenerationID}
		if n, err := kw.Count(ctx, ns.Name, filter); err != nil || n > 0 {
			continue
		}
		gen, err := store.GetGeneration(ctx, p.GenerationID)
		if err != nil {
			continue
		}
		var sources []registry.Source
		if m, ok := reg.Match(gen.Dependency.Dependency.Ecosystem, gen.Dependency.Dependency.Name); ok {
			sources = m.Sources
		}
		if err := generation.AddToIndex(ctx, gen, sources, nil, kw, ns, badgerStore); err != nil {
			return fmt.Errorf("add keyword entries for %s %s: %w", gen.Dependency.Dependency.Name, gen.Dependency.Version, err)
		}
		if n, _ := kw.Count(ctx, ns.Name, filter); n > 0 {
			fmt.Fprintf(out, "%-12s %s %s (added to the keyword index)\n", "KEYWORD", gen.Dependency.Dependency.Name, gen.Dependency.Version)
		}
	}
	return nil
}

// hasKeywordOnlyGenerations reports, from the store alone, whether any
// active generation was built without vectors.
func hasKeywordOnlyGenerations(ctx context.Context, store *bboltstore.Store, cfg config.Config) bool {
	pointers, err := store.ListActivePointers(ctx, cfg.Vector.Backend)
	if err != nil {
		return false
	}
	for _, p := range pointers {
		if replica, err := store.GetBackendReplica(ctx, p.GenerationID, cfg.Vector.Backend); err == nil && replica.EmbeddingModel == "" {
			return true
		}
	}
	return false
}

// backfillVectors adds vectors to active generations built while the
// embedder was unavailable (auto mode). Their chunks are already stored,
// so this only embeds and writes vectors: nothing is fetched or rebuilt.
// A generation built without vectors is the one whose replica records no
// embedding model.
func backfillVectors(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, reg *registry.Registry, embedder embedding.Embedder, vb backend.VectorBackend, ns backend.Namespace, out io.Writer) error {
	pointers, err := store.ListActivePointers(ctx, cfg.Vector.Backend)
	if err != nil {
		return fmt.Errorf("list active generations: %w", err)
	}
	for _, p := range pointers {
		replica, err := store.GetBackendReplica(ctx, p.GenerationID, cfg.Vector.Backend)
		if err != nil || replica.EmbeddingModel != "" {
			continue
		}
		gen, err := store.GetGeneration(ctx, p.GenerationID)
		if err != nil {
			continue
		}
		var sources []registry.Source
		if m, ok := reg.Match(gen.Dependency.Dependency.Ecosystem, gen.Dependency.Dependency.Name); ok {
			sources = m.Sources
		}
		if err := generation.AddToIndex(ctx, gen, sources, embedder, vb, ns, badgerStore); err != nil {
			return fmt.Errorf("add vectors to %s %s: %w", gen.Dependency.Dependency.Name, gen.Dependency.Version, err)
		}
		// Recorded only after every vector is written.
		replica.EmbeddingModel = embedder.ModelID()
		replica.Dimensions = ns.Dimensions
		replica.UpdatedAt = time.Now()
		if err := store.PutBackendReplica(ctx, replica); err != nil {
			return fmt.Errorf("record vectors for %s %s: %w", gen.Dependency.Dependency.Name, gen.Dependency.Version, err)
		}
		fmt.Fprintf(out, "%-12s %s %s (added vectors; it was synced while embeddings were unavailable)\n", "VECTORS", gen.Dependency.Dependency.Name, gen.Dependency.Version)
	}
	return nil
}

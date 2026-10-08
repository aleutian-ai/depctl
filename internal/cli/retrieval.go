package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/keyword"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/registry"
)

// readinessSettleTimeout is how long auto mode waits for a readiness probe
// that's still running before deciding without it, so a sync right at
// daemon startup doesn't needlessly build keyword-only.
const readinessSettleTimeout = 10 * time.Second

// namespaceDropper is a vector store that can delete a whole namespace.
// Only the embedded store implements it: its file is this install's
// alone, while a remote collection may be shared with another install.
type namespaceDropper interface {
	DropNamespace(ctx context.Context, name string) error
}

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

// Query searches every index the request can use. With a query vector
// and both indexes (auto mode with the embedder ready), it runs a hybrid
// search: each index's top candidates merged by reciprocal rank fusion,
// which measured better than either alone (docs/retrieval-eval.md).
// Score is then the fused score, comparable only within one result. A
// generation built without vectors contributes no vector candidates, so
// its results are keyword search's. With only one usable index, that
// index answers alone.
func (s *searchIndex) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	useVector := s.vector != nil && req.Vector != nil
	switch {
	case useVector && s.keyword != nil:
		return s.hybrid(ctx, req)
	case useVector:
		return s.vector.Query(ctx, req)
	case s.keyword != nil:
		return s.keyword.Query(ctx, req)
	default:
		return backend.QueryResult{}, fmt.Errorf("retrieval.mode is vector, but no query vector was given (is the embedder ready?)")
	}
}

// hybridCandidates is how many results each index contributes to fusion,
// and rrfK the usual reciprocal-rank-fusion constant; both as evaluated.
const (
	hybridCandidates = 50
	rrfK             = 60
)

// hybrid fuses the keyword and vector rankings: each point scores the sum
// of 1/(rrfK + rank) over the lists it appears in.
func (s *searchIndex) hybrid(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = 10
	}
	wide := req
	wide.TopK = max(topK, hybridCandidates)
	type key struct{ generation, id string }
	fused := map[key]*backend.ScoredPoint{}
	for _, b := range []backend.VectorBackend{s.keyword, s.vector} {
		res, err := b.Query(ctx, wide)
		if err != nil {
			return backend.QueryResult{}, err
		}
		for rank, p := range res.Points {
			k := key{p.Metadata.Generation, p.ID}
			if fused[k] == nil {
				p := p
				p.Score = 0
				fused[k] = &p
			}
			fused[k].Score += float32(1 / float64(rrfK+rank+1))
		}
	}
	out := make([]backend.ScoredPoint, 0, len(fused))
	for _, p := range fused {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return backend.QueryResult{Points: out}, nil
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

// backfillVectors adds vectors to active generations that have none:
// built while the embedder was unavailable (auto mode), or cleared by
// switchEmbedding. Their chunks are already stored,
// so this only embeds and writes vectors: nothing is fetched or rebuilt.
// A generation built without vectors is the one whose replica records no
// embedding model.
func backfillVectors(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, reg *registry.Registry, embedder *embedding.Prompted, vb backend.VectorBackend, ns backend.Namespace, out io.Writer) error {
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
		fmt.Fprintf(out, "%-12s %s %s (added vectors from its stored chunks)\n", "VECTORS", gen.Dependency.Dependency.Name, gen.Dependency.Version)
	}
	return nil
}

// staleVectorGenerations returns the replicas of active generations whose
// vectors were made with other embedding settings (model, prompts or
// size) than cfg's. Generations built without vectors aren't stale.
func staleVectorGenerations(ctx context.Context, store *bboltstore.Store, cfg config.Config) ([]domain.BackendReplica, error) {
	pointers, err := store.ListActivePointers(ctx, cfg.Vector.Backend)
	if err != nil {
		return nil, fmt.Errorf("list active generations: %w", err)
	}
	want := cfg.Embedding.Prompts().Identity(cfg.Embedding.Model)
	var stale []domain.BackendReplica
	for _, p := range pointers {
		replica, err := store.GetBackendReplica(ctx, p.GenerationID, cfg.Vector.Backend)
		if err == nil && replica.EmbeddingModel != "" && replica.EmbeddingModel != want {
			stale = append(stale, replica)
		}
	}
	return stale, nil
}

// hasStaleVectors reports whether the embedding settings changed since
// some active generation was embedded. Until a sync re-embeds them, their
// vectors can't be compared with a question's, so search doesn't use
// vectors at all.
func hasStaleVectors(ctx context.Context, store *bboltstore.Store, cfg config.Config) bool {
	stale, err := staleVectorGenerations(ctx, store, cfg)
	return err == nil && len(stale) > 0
}

// errStaleVectors is vector mode's answer while active generations hold
// vectors made with other embedding settings.
var errStaleVectors = errors.New("the embedding settings changed since some versions were embedded; the next `ragctl sync` re-embeds them (it reuses their stored chunks)")

// switchMu keeps concurrent syncs from switching at once: a second drop
// could delete vectors the first had already re-embedded.
var switchMu sync.Mutex

// switchEmbeddingOnce is switchEmbedding under switchMu; a sync that waited
// finds nothing stale left.
func switchEmbeddingOnce(ctx context.Context, store *bboltstore.Store, cfg config.Config, vb backend.VectorBackend, ns backend.Namespace, out io.Writer) error {
	switchMu.Lock()
	defer switchMu.Unlock()
	return switchEmbedding(ctx, store, cfg, vb, ns, out)
}

// switchEmbedding clears vectors made with other embedding settings than
// cfg's, and marks their generations as built without vectors, so
// backfillVectors re-embeds them. Their chunks are already stored:
// nothing is fetched or rebuilt, and keyword search covers them
// meanwhile.
//
// The embedded store drops the whole namespace, since its file is this
// install's alone. A remote collection may be shared with another
// install, so only this install's points are deleted, and if the vector
// size changed while the collection still holds them, nothing is changed:
// a collection has one size, so the user picks a new vector.collection.
func switchEmbedding(ctx context.Context, store *bboltstore.Store, cfg config.Config, vb backend.VectorBackend, ns backend.Namespace, out io.Writer) error {
	stale, err := staleVectorGenerations(ctx, store, cfg)
	if err != nil || len(stale) == 0 {
		return err
	}
	dropper, owned := vb.(namespaceDropper)
	if owned {
		// Dropped before any replica is cleared: if this stops part way,
		// the rest are still stale and the next sync drops again.
		if err := dropper.DropNamespace(ctx, ns.Name); err != nil {
			return fmt.Errorf("drop old vectors: %w", err)
		}
		if err := vb.EnsureNamespace(ctx, ns); err != nil {
			return err
		}
	} else {
		for _, r := range stale {
			if r.Dimensions == ns.Dimensions {
				continue
			}
			n, err := vb.Count(ctx, ns.Name, &backend.Filter{Generation: r.GenerationID})
			if err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("the embedding size changed from %d to %d, and collection %q at %s still holds this install's old vectors; a collection has one size, so set vector.collection to a new name and sync again (then delete %q if no other install uses it)",
					r.Dimensions, ns.Dimensions, ns.Name, vectorLocation(cfg), ns.Name)
			}
		}
	}
	for _, r := range stale {
		if !owned {
			if err := vb.Delete(ctx, backend.DeleteRequest{Namespace: ns.Name, Filter: &backend.Filter{Generation: r.GenerationID}}); err != nil {
				return fmt.Errorf("delete old vectors: %w", err)
			}
		}
		r.EmbeddingModel, r.Dimensions, r.UpdatedAt = "", 0, time.Now()
		if err := store.PutBackendReplica(ctx, r); err != nil {
			return fmt.Errorf("record old vectors removed: %w", err)
		}
	}
	fmt.Fprintf(out, "%-12s embedding settings changed: re-embedding %d version(s) with %q (keyword search covers them meanwhile)\n",
		"VECTORS", len(stale), cfg.Embedding.Prompts().Identity(cfg.Embedding.Model))
	return nil
}

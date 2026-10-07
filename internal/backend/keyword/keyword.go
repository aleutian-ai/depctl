// Package keyword implements backend.VectorBackend as keyword search
// (BM25) in a single bbolt file, so search needs no embedding model
// (LOCAL-001). Like the embedded vector store it scopes first and ranks
// second: a search is filtered to one dependency version (hundreds to a
// few thousand chunks), so BM25 is computed over exactly those chunks at
// query time and no inverted index is kept.
//
// Layout, per namespace bucket: "generations" maps each generation ID to
// its ecosystem/dependency/version (a generation is one dependency
// version, so they're stored once), and "chunks" holds one bucket per
// generation, keyed by chunk ID. A point's identity (generation, chunk
// ID) is its location, so no separate index is needed, and deleting a
// generation is one bucket delete.
package keyword

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/backend"
)

const (
	defaultTopK = 10
	// BM25's standard parameters: term-frequency saturation and
	// document-length normalization.
	k1 = 1.2
	b  = 0.75
	// openTimeout bounds waiting for the file lock (see embedded).
	openTimeout = 2 * time.Second
	// fillPercent packs pages full: a generation's chunks are written in
	// key order (sorted per batch, and listed in order by Badger), so its
	// bucket only ever grows at the end, where bbolt's default half-full
	// split would just waste space.
	fillPercent = 1.0
)

// Within a namespace's bucket (see the package comment).
var (
	generationsKey = []byte("generations")
	chunksKey      = []byte("chunks")
)

// dbs holds one open handle per file per process.
var (
	dbsMu sync.Mutex
	dbs   = map[string]*bolt.DB{}
)

// Store is a keyword-search backend backed by one bbolt file.
type Store struct {
	path string
}

// New returns a Store for the file at path, created on first use.
func New(path string) *Store {
	return &Store{path: path}
}

// Name identifies this backend.
func (s *Store) Name() string { return "keyword" }

// Capabilities reports keyword search, metadata filtering and
// delete-by-filter; no vector search.
func (s *Store) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	return backend.Capabilities{KeywordSearch: true, MetadataFilter: true, DeleteByFilter: true}, nil
}

// Health checks the file can be opened and read.
func (s *Store) Health(ctx context.Context) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.View(func(tx *bolt.Tx) error { return nil })
}

// EnsureNamespace creates ns's bucket if missing. Idempotent. Dimensions
// and distance are ignored: nothing here is a vector.
func (s *Store) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(ns.Name))
		if err != nil {
			return fmt.Errorf("keyword: create namespace %s: %w", ns.Name, err)
		}
		if _, err := bk.CreateBucketIfNotExists(generationsKey); err != nil {
			return err
		}
		_, err = bk.CreateBucketIfNotExists(chunksKey)
		return err
	})
}

// Upsert indexes req.Points' Text in one transaction. Every point of a
// generation must carry the same ecosystem/dependency/version (the
// backend.Point contract); a point that contradicts its generation's is
// an error, not a silent relabel.
func (s *Store) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	points := append([]backend.Point(nil), req.Points...)
	sort.Slice(points, func(i, j int) bool {
		if points[i].Metadata.Generation != points[j].Metadata.Generation {
			return points[i].Metadata.Generation < points[j].Metadata.Generation
		}
		return points[i].ID < points[j].ID
	})
	return db.Update(func(tx *bolt.Tx) error {
		bk, err := namespace(tx, req.Namespace)
		if err != nil {
			return err
		}
		for _, p := range points {
			chunks, err := generationBucket(bk, p.Metadata)
			if err != nil {
				return err
			}
			if err := chunks.Put([]byte(p.ID), encodeValue(p.Metadata, Tokenize(p.Text))); err != nil {
				return err
			}
		}
		return nil
	})
}

// Delete removes points matching req.IDs (in every generation) or
// req.Filter (a union). A request with no IDs and an empty filter
// deletes nothing.
func (s *Store) Delete(ctx context.Context, req backend.DeleteRequest) error {
	byFilter := req.Filter != nil && *req.Filter != (backend.Filter{})
	if len(req.IDs) == 0 && !byFilter {
		return nil
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	wanted := make(map[string]bool, len(req.IDs))
	for _, id := range req.IDs {
		wanted[id] = true
	}
	return db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(req.Namespace))
		if bk == nil {
			return nil // never created: nothing to delete
		}
		// Collect first: bbolt can't delete buckets mid-iteration.
		var gone []string
		err := forEachGeneration(bk, nil, func(gen string, _ backend.PointMetadata, chunks *bolt.Bucket) error {
			if byFilter && matchesFilter(gen, generationMetadata(bk, gen), req.Filter) {
				gone = append(gone, gen)
				return nil
			}
			for id := range wanted {
				if err := chunks.Delete([]byte(id)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, gen := range gone {
			if err := bk.Bucket(chunksKey).DeleteBucket([]byte(gen)); err != nil {
				return err
			}
			if err := bk.Bucket(generationsKey).Delete([]byte(gen)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Query returns the TopK points that best match req.Text by BM25, among
// those req.Filter selects. Points matching no query term aren't
// returned. With no searchable terms in Text, it returns the selected
// points unranked (score 0) in key order.
func (s *Store) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	db, err := s.db()
	if err != nil {
		return backend.QueryResult{}, err
	}
	queryTerms := map[string]bool{}
	for _, t := range Tokenize(req.Text) {
		queryTerms[t] = true
	}

	type doc struct {
		point  backend.ScoredPoint
		length int
		tf     map[string]int // only the query's terms
	}
	var docs []doc
	err = db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(req.Namespace))
		if bk == nil {
			return nil // nothing indexed yet: no matches
		}
		return forEachGeneration(bk, req.Filter, func(gen string, meta backend.PointMetadata, chunks *bolt.Bucket) error {
			return chunks.ForEach(func(k, v []byte) error {
				m, length, tf := decodeValue(v, queryTerms)
				m.Ecosystem, m.Dependency, m.Version, m.Generation = meta.Ecosystem, meta.Dependency, meta.Version, gen
				docs = append(docs, doc{point: backend.ScoredPoint{ID: string(k), Metadata: m}, length: length, tf: tf})
				return nil
			})
		})
	})
	if err != nil {
		return backend.QueryResult{}, err
	}

	if len(queryTerms) == 0 {
		var res backend.QueryResult
		for i := 0; i < len(docs) && i < topK; i++ {
			res.Points = append(res.Points, docs[i].point)
		}
		return res, nil
	}

	// BM25 statistics come from the selected points only: the corpus is
	// the dependency version being searched, not everything in the file.
	n := float64(len(docs))
	df := map[string]int{}
	total := 0
	for _, d := range docs {
		total += d.length
		for t := range d.tf {
			df[t]++
		}
	}
	avgLen := 1.0
	if len(docs) > 0 && total > 0 {
		avgLen = float64(total) / n
	}
	var hits []backend.ScoredPoint
	for _, d := range docs {
		var score float64
		for t, tf := range d.tf {
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			f := float64(tf)
			score += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(d.length)/avgLen))
		}
		if score > 0 {
			d.point.Score = float32(score)
			hits = append(hits, d.point)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return backend.QueryResult{Points: hits}, nil
}

// Count reports exactly how many points in namespace match filter; a
// namespace nothing was ever indexed into has none.
func (s *Store) Count(ctx context.Context, ns string, filter *backend.Filter) (int, error) {
	db, err := s.db()
	if err != nil {
		return 0, err
	}
	n := 0
	err = db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(ns))
		if bk == nil {
			return nil
		}
		return forEachGeneration(bk, filter, func(_ string, _ backend.PointMetadata, chunks *bolt.Bucket) error {
			n += chunks.Stats().KeyN
			return nil
		})
	})
	return n, err
}

// db opens the file on first use, sharing one handle per path.
func (s *Store) db() (*bolt.DB, error) {
	dbsMu.Lock()
	defer dbsMu.Unlock()
	if db, ok := dbs[s.path]; ok {
		return db, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return nil, fmt.Errorf("keyword: create directory for %s: %w", s.path, err)
	}
	db, err := bolt.Open(s.path, 0o600, &bolt.Options{Timeout: openTimeout})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, fmt.Errorf("keyword: %s is in use by another ragctl process (normally the daemon)", s.path)
	}
	if err != nil {
		return nil, fmt.Errorf("keyword: open %s: %w", s.path, err)
	}
	if db, err = migrateIfNeeded(s.path, db); err != nil {
		return nil, err
	}
	dbs[s.path] = db
	return db, nil
}

func namespace(tx *bolt.Tx, name string) (*bolt.Bucket, error) {
	bk := tx.Bucket([]byte(name))
	if bk == nil {
		return nil, fmt.Errorf("keyword: namespace %s doesn't exist", name)
	}
	return bk, nil
}

// generationBucket returns the chunk bucket for m's generation, creating
// it (and recording the generation's metadata) on first use.
func generationBucket(bk *bolt.Bucket, m backend.PointMetadata) (*bolt.Bucket, error) {
	meta, err := encodeMeta(m)
	if err != nil {
		return nil, err
	}
	gens := bk.Bucket(generationsKey)
	gen := []byte(m.Generation)
	if have := gens.Get(gen); have == nil {
		if err := gens.Put(gen, meta); err != nil {
			return nil, err
		}
	} else if !bytes.Equal(have, meta) {
		return nil, fmt.Errorf("keyword: generation %s is %q, but a point for it says %q (a generation is one dependency version)", m.Generation, strings.ReplaceAll(string(have), "\x00", " "), strings.ReplaceAll(string(meta), "\x00", " "))
	}
	chunks, err := bk.Bucket(chunksKey).CreateBucketIfNotExists(gen)
	if err != nil {
		return nil, err
	}
	chunks.FillPercent = fillPercent
	return chunks, nil
}

// forEachGeneration calls fn for every generation f selects (nil selects
// all), with the generation's metadata and chunk bucket. A filter naming
// a generation is a direct lookup; otherwise only the small generations
// bucket is scanned to find matches.
func forEachGeneration(bk *bolt.Bucket, f *backend.Filter, fn func(gen string, meta backend.PointMetadata, chunks *bolt.Bucket) error) error {
	visit := func(gen string) error {
		chunks := bk.Bucket(chunksKey).Bucket([]byte(gen))
		if chunks == nil {
			return nil
		}
		meta := generationMetadata(bk, gen)
		if f != nil && !matchesFilter(gen, meta, f) {
			return nil
		}
		return fn(gen, meta, chunks)
	}
	if f != nil && f.Generation != "" {
		return visit(f.Generation)
	}
	var gens []string
	if err := bk.Bucket(generationsKey).ForEach(func(k, _ []byte) error {
		gens = append(gens, string(k))
		return nil
	}); err != nil {
		return err
	}
	for _, gen := range gens {
		if err := visit(gen); err != nil {
			return err
		}
	}
	return nil
}

// generationMetadata reads gen's ecosystem/dependency/version.
func generationMetadata(bk *bolt.Bucket, gen string) backend.PointMetadata {
	f := strings.Split(string(bk.Bucket(generationsKey).Get([]byte(gen))), "\x00")
	for len(f) < 3 {
		f = append(f, "")
	}
	return backend.PointMetadata{Ecosystem: f[0], Dependency: f[1], Version: f[2], Generation: gen}
}

// matchesFilter reports whether a generation satisfies every non-empty
// field of f: whole-value matches only.
func matchesFilter(gen string, m backend.PointMetadata, f *backend.Filter) bool {
	return (f.Ecosystem == "" || f.Ecosystem == m.Ecosystem) &&
		(f.Dependency == "" || f.Dependency == m.Dependency) &&
		(f.Version == "" || f.Version == m.Version) &&
		(f.Generation == "" || f.Generation == gen)
}

func encodeMeta(m backend.PointMetadata) ([]byte, error) {
	for _, v := range []string{m.Ecosystem, m.Dependency, m.Version, m.Generation} {
		if strings.Contains(v, "\x00") {
			return nil, fmt.Errorf("keyword: metadata value %q contains a NUL byte", v)
		}
	}
	return []byte(m.Ecosystem + "\x00" + m.Dependency + "\x00" + m.Version), nil
}

// encodeValue stores, as varints: source type, authority, the document's
// length in terms, and each distinct term with its count. Varints, not
// fixed widths: per-term counts and lengths are small, and fixed widths
// cost more than the terms themselves.
func encodeValue(m backend.PointMetadata, terms []string) []byte {
	tf := map[string]int{}
	for _, t := range terms {
		tf[t]++
	}
	out := binary.AppendUvarint(nil, uint64(len(m.SourceType)))
	out = append(out, m.SourceType...)
	out = binary.AppendVarint(out, int64(m.Authority))
	out = binary.AppendUvarint(out, uint64(len(terms)))
	out = binary.AppendUvarint(out, uint64(len(tf)))
	for t, n := range tf {
		out = binary.AppendUvarint(out, uint64(len(t)))
		out = append(out, t...)
		out = binary.AppendUvarint(out, uint64(n))
	}
	return out
}

// decodeValue returns the metadata, document length, and the counts of
// just the terms in want (all a query needs).
func decodeValue(v []byte, want map[string]bool) (backend.PointMetadata, int, map[string]int) {
	r := varintReader{b: v}
	m := backend.PointMetadata{SourceType: string(r.bytes(int(r.uvarint())))}
	m.Authority = int(r.varint())
	length := int(r.uvarint())
	distinct := int(r.uvarint())
	tf := map[string]int{}
	for range distinct {
		term := r.bytes(int(r.uvarint()))
		count := int(r.uvarint())
		if want[string(term)] {
			tf[string(term)] = count
		}
	}
	return m, length, tf
}

// varintReader walks an encodeValue value.
type varintReader struct{ b []byte }

func (r *varintReader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	r.b = r.b[n:]
	return v
}

func (r *varintReader) varint() int64 {
	v, n := binary.Varint(r.b)
	r.b = r.b[n:]
	return v
}

func (r *varintReader) bytes(n int) []byte {
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

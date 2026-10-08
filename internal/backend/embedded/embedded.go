// Package embedded implements backend.VectorBackend in a single bbolt
// file inside ragctl's data directory (VEC-015), so no vector service has
// to run. Search is exact: brute-force cosine similarity over the points
// a filter selects. ragctl's searches are always scoped to one
// dependency version (hundreds of chunks), so no approximate index is
// needed.
//
// Layout, per namespace bucket: "dims" holds the vector dimension,
// "generations" maps each generation ID to its ecosystem/dependency/
// version (a generation is one dependency version, so they're stored
// once), and "chunks" holds one bucket per generation, keyed by chunk
// ID. A point's identity (generation, chunk ID) is its location, so no
// separate index is needed, and deleting a generation is one bucket
// delete.
package embedded

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

// ErrDimensionMismatch means a namespace exists with a different vector
// dimension than requested (e.g. the embedding model changed).
var ErrDimensionMismatch = errors.New("embedded: dimension mismatch")

const (
	defaultTopK = 10
	// openTimeout bounds waiting for the file lock. bbolt locks the file
	// per open handle, so a second process (or a second handle in this
	// one) would otherwise wait forever.
	openTimeout = 2 * time.Second
	// fillPercent packs pages full: a generation's chunks are written in
	// key order (sorted per batch, and listed in order by Badger), so its
	// bucket only ever grows at the end, where bbolt's default half-full
	// split would waste about half of every page.
	fillPercent = 1.0
)

// Within a namespace's bucket (see the package comment).
var (
	dimsKey        = []byte("dims")
	generationsKey = []byte("generations")
	chunksKey      = []byte("chunks")
)

// dbs holds one open handle per file per process: every backend built
// for the same path shares it.
var (
	dbsMu sync.Mutex
	dbs   = map[string]*bolt.DB{}
)

// Store is a VectorBackend backed by one bbolt file.
type Store struct {
	path string
}

// New returns a Store for the file at path, created on first use.
func New(path string) *Store {
	return &Store{path: path}
}

// Name identifies this backend.
func (s *Store) Name() string { return "embedded" }

// Capabilities reports vector search, metadata filtering, and
// delete-by-filter.
func (s *Store) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	return backend.Capabilities{VectorSearch: true, MetadataFilter: true, DeleteByFilter: true}, nil
}

// Health checks the file can be opened and read.
func (s *Store) Health(ctx context.Context) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.View(func(tx *bolt.Tx) error { return nil })
}

// EnsureNamespace creates ns's bucket if missing. Idempotent; an existing
// namespace with a different dimension is ErrDimensionMismatch.
func (s *Store) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	if ns.Distance != "" && !strings.EqualFold(ns.Distance, "cosine") {
		return fmt.Errorf("embedded: unsupported distance %q (only cosine)", ns.Distance)
	}
	if ns.Dimensions <= 0 {
		return fmt.Errorf("embedded: namespace %q needs a positive dimension", ns.Name)
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(ns.Name))
		if err != nil {
			return fmt.Errorf("embedded: create namespace %s: %w", ns.Name, err)
		}
		if raw := b.Get(dimsKey); raw != nil {
			if have := int(binary.LittleEndian.Uint32(raw)); have != ns.Dimensions {
				return fmt.Errorf("%w: namespace %s has %d dimensions, want %d", ErrDimensionMismatch, ns.Name, have, ns.Dimensions)
			}
			return nil
		}
		if err := b.Put(dimsKey, binary.LittleEndian.AppendUint32(nil, uint32(ns.Dimensions))); err != nil {
			return err
		}
		if _, err := b.CreateBucketIfNotExists(generationsKey); err != nil {
			return err
		}
		_, err = b.CreateBucketIfNotExists(chunksKey)
		return err
	})
}

// Upsert writes or overwrites req.Points in one transaction: a bad point
// (the wrong dimension, or metadata contradicting its generation's)
// means nothing in the request is written.
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
		b, err := namespace(tx, req.Namespace)
		if err != nil {
			return err
		}
		dims := int(binary.LittleEndian.Uint32(b.Get(dimsKey)))
		for _, p := range points {
			if len(p.Vector) != dims {
				return fmt.Errorf("%w: point %s has %d dimensions, namespace %s has %d", ErrDimensionMismatch, p.ID, len(p.Vector), req.Namespace, dims)
			}
			chunks, err := generationBucket(b, p.Metadata)
			if err != nil {
				return err
			}
			if err := chunks.Put([]byte(p.ID), encodeValue(p.Metadata, p.Vector)); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropNamespace deletes the namespace and every point in it, including
// its dimension, so it can be recreated at another size. Dropping a
// namespace that doesn't exist is a no-op.
func (s *Store) DropNamespace(ctx context.Context, name string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(name)) == nil {
			return nil
		}
		return tx.DeleteBucket([]byte(name))
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
		b := tx.Bucket([]byte(req.Namespace))
		if b == nil {
			return nil // never created: nothing to delete
		}
		// Collect first: bbolt can't delete buckets mid-iteration.
		var gone []string
		err := forEachGeneration(b, nil, func(gen string, meta backend.PointMetadata, chunks *bolt.Bucket) error {
			if byFilter && matchesFilter(gen, meta, req.Filter) {
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
			if err := b.Bucket(chunksKey).DeleteBucket([]byte(gen)); err != nil {
				return err
			}
			if err := b.Bucket(generationsKey).Delete([]byte(gen)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Query returns the TopK points most cosine-similar to req.Vector among
// those req.Filter selects. Score is cosine similarity, higher is better.
func (s *Store) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	db, err := s.db()
	if err != nil {
		return backend.QueryResult{}, err
	}
	qNorm := norm(req.Vector)
	var hits []backend.ScoredPoint
	err = db.View(func(tx *bolt.Tx) error {
		b, err := namespace(tx, req.Namespace)
		if err != nil {
			return err
		}
		if dims := int(binary.LittleEndian.Uint32(b.Get(dimsKey))); len(req.Vector) != dims {
			return fmt.Errorf("%w: query vector has %d dimensions, namespace %s has %d", ErrDimensionMismatch, len(req.Vector), req.Namespace, dims)
		}
		return forEachGeneration(b, req.Filter, func(gen string, meta backend.PointMetadata, chunks *bolt.Bucket) error {
			return chunks.ForEach(func(k, v []byte) error {
				m, vec := decodeValue(v)
				m.Ecosystem, m.Dependency, m.Version, m.Generation = meta.Ecosystem, meta.Dependency, meta.Version, gen
				var score float32
				if n := qNorm * norm(vec); n > 0 {
					score = dot(req.Vector, vec) / n
				}
				hits = append(hits, backend.ScoredPoint{ID: string(k), Score: score, Metadata: m})
				return nil
			})
		})
	})
	if err != nil {
		return backend.QueryResult{}, err
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return backend.QueryResult{Points: hits}, nil
}

// Count reports exactly how many points in namespace match filter.
func (s *Store) Count(ctx context.Context, ns string, filter *backend.Filter) (int, error) {
	db, err := s.db()
	if err != nil {
		return 0, err
	}
	n := 0
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(ns))
		if b == nil {
			return nil // never created: no points
		}
		return forEachGeneration(b, filter, func(_ string, _ backend.PointMetadata, chunks *bolt.Bucket) error {
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
		return nil, fmt.Errorf("embedded: create directory for %s: %w", s.path, err)
	}
	db, err := bolt.Open(s.path, 0o600, &bolt.Options{Timeout: openTimeout})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, fmt.Errorf("embedded: %s is in use by another ragctl process (normally the daemon)", s.path)
	}
	if err != nil {
		return nil, fmt.Errorf("embedded: open %s: %w", s.path, err)
	}
	if db, err = migrateIfNeeded(s.path, db); err != nil {
		return nil, err
	}
	dbs[s.path] = db
	return db, nil
}

func namespace(tx *bolt.Tx, name string) (*bolt.Bucket, error) {
	b := tx.Bucket([]byte(name))
	if b == nil {
		return nil, fmt.Errorf("embedded: namespace %s doesn't exist", name)
	}
	return b, nil
}

// generationBucket returns the chunk bucket for m's generation, creating
// it (and recording the generation's metadata) on first use.
func generationBucket(b *bolt.Bucket, m backend.PointMetadata) (*bolt.Bucket, error) {
	meta, err := encodeMeta(m)
	if err != nil {
		return nil, err
	}
	gens := b.Bucket(generationsKey)
	gen := []byte(m.Generation)
	if have := gens.Get(gen); have == nil {
		if err := gens.Put(gen, meta); err != nil {
			return nil, err
		}
	} else if !bytes.Equal(have, meta) {
		return nil, fmt.Errorf("embedded: generation %s is %q, but a point for it says %q (a generation is one dependency version)", m.Generation, strings.ReplaceAll(string(have), "\x00", " "), strings.ReplaceAll(string(meta), "\x00", " "))
	}
	chunks, err := b.Bucket(chunksKey).CreateBucketIfNotExists(gen)
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
func forEachGeneration(b *bolt.Bucket, f *backend.Filter, fn func(gen string, meta backend.PointMetadata, chunks *bolt.Bucket) error) error {
	visit := func(gen string) error {
		chunks := b.Bucket(chunksKey).Bucket([]byte(gen))
		if chunks == nil {
			return nil
		}
		meta := generationMetadata(b, gen)
		if f != nil && !matchesFilter(gen, meta, f) {
			return nil
		}
		return fn(gen, meta, chunks)
	}
	if f != nil && f.Generation != "" {
		return visit(f.Generation)
	}
	var gens []string
	if err := b.Bucket(generationsKey).ForEach(func(k, _ []byte) error {
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
func generationMetadata(b *bolt.Bucket, gen string) backend.PointMetadata {
	f := strings.Split(string(b.Bucket(generationsKey).Get([]byte(gen))), "\x00")
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
			return nil, fmt.Errorf("embedded: metadata value %q contains a NUL byte", v)
		}
	}
	return []byte(m.Ecosystem + "\x00" + m.Dependency + "\x00" + m.Version), nil
}

// encodeValue stores source type, authority, and the vector, as
// little-endian binary (the vector is nearly all of it).
func encodeValue(m backend.PointMetadata, vec []float32) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(m.SourceType)))
	out = append(out, m.SourceType...)
	out = binary.LittleEndian.AppendUint64(out, uint64(int64(m.Authority)))
	for _, x := range vec {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(x))
	}
	return out
}

func decodeValue(v []byte) (backend.PointMetadata, []float32) {
	n := int(binary.LittleEndian.Uint32(v))
	m := backend.PointMetadata{SourceType: string(v[4 : 4+n])}
	v = v[4+n:]
	m.Authority = int(int64(binary.LittleEndian.Uint64(v)))
	v = v[8:]
	vec := make([]float32, len(v)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(v[4*i:]))
	}
	return m, vec
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range min(len(a), len(b)) {
		s += a[i] * b[i]
	}
	return s
}

func norm(a []float32) float32 {
	return float32(math.Sqrt(float64(dot(a, a))))
}

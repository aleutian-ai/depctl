// Package embedded implements backend.VectorBackend in a single bbolt
// file inside ragctl's data directory (VEC-015), so no vector service has
// to run. Search is exact: brute-force cosine similarity over the points
// a filter selects. ragctl's searches are always scoped to one
// dependency version (hundreds of chunks), so no approximate index is
// needed.
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
)

// Within a namespace's bucket: the dimension, all points, and an index
// from a point's identity (generation, chunk ID) to its points key.
var (
	dimsKey   = []byte("dims")
	pointsKey = []byte("points")
	idsKey    = []byte("ids")
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
		if _, err := b.CreateBucketIfNotExists(pointsKey); err != nil {
			return err
		}
		_, err = b.CreateBucketIfNotExists(idsKey)
		return err
	})
}

// Upsert writes or overwrites req.Points in one transaction: a bad point
// (e.g. the wrong dimension) means nothing in the request is written.
func (s *Store) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		b, err := namespace(tx, req.Namespace)
		if err != nil {
			return err
		}
		dims := int(binary.LittleEndian.Uint32(b.Get(dimsKey)))
		points, ids := b.Bucket(pointsKey), b.Bucket(idsKey)
		for _, p := range req.Points {
			if len(p.Vector) != dims {
				return fmt.Errorf("%w: point %s has %d dimensions, namespace %s has %d", ErrDimensionMismatch, p.ID, len(p.Vector), req.Namespace, dims)
			}
			m := p.Metadata
			key, err := joinKey(m.Ecosystem, m.Dependency, m.Version, m.Generation, p.ID)
			if err != nil {
				return err
			}
			// The same (generation, ID) re-written with different
			// metadata must replace the old entry, not sit beside it.
			identity := []byte(m.Generation + "\x00" + p.ID)
			if old := ids.Get(identity); old != nil && !bytes.Equal(old, key) {
				if err := points.Delete(old); err != nil {
					return err
				}
			}
			if err := points.Put(key, encodeValue(m, p.Vector)); err != nil {
				return err
			}
			if err := ids.Put(identity, key); err != nil {
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
		b, err := namespace(tx, req.Namespace)
		if err != nil {
			return err
		}
		points, ids := b.Bucket(pointsKey), b.Bucket(idsKey)
		// Collect first: bbolt cursors don't survive deletes mid-scan.
		var doomed [][]byte
		c := points.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			f := strings.Split(string(k), "\x00")
			if wanted[f[4]] || (byFilter && matches(f, req.Filter)) {
				doomed = append(doomed, bytes.Clone(k))
			}
		}
		for _, k := range doomed {
			f := strings.Split(string(k), "\x00")
			if err := points.Delete(k); err != nil {
				return err
			}
			if err := ids.Delete([]byte(f[3] + "\x00" + f[4])); err != nil {
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
		return scan(b.Bucket(pointsKey), req.Filter, func(f []string, v []byte) {
			m, vec := decodeValue(v)
			m.Ecosystem, m.Dependency, m.Version, m.Generation = f[0], f[1], f[2], f[3]
			var score float32
			if n := qNorm * norm(vec); n > 0 {
				score = dot(req.Vector, vec) / n
			}
			hits = append(hits, backend.ScoredPoint{ID: f[4], Score: score, Metadata: m})
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
		b, err := namespace(tx, ns)
		if err != nil {
			return err
		}
		return scan(b.Bucket(pointsKey), filter, func([]string, []byte) { n++ })
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

// scan calls fn with the key fields and value of every point f selects.
// Keys are ecosystem\0dependency\0version\0generation\0id, so the leading
// filter fields that are set become a prefix and the scan touches only
// those points (normally one dependency version).
func scan(points *bolt.Bucket, f *backend.Filter, fn func(fields []string, v []byte)) error {
	var prefix []byte
	if f != nil {
		for _, v := range []string{f.Ecosystem, f.Dependency, f.Version, f.Generation} {
			if v == "" {
				break
			}
			prefix = append(append(prefix, v...), 0)
		}
	}
	c := points.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		fields := strings.Split(string(k), "\x00")
		if f == nil || matches(fields, f) {
			fn(fields, v)
		}
	}
	return nil
}

// matches reports whether key fields satisfy every non-empty field of f.
func matches(fields []string, f *backend.Filter) bool {
	for i, want := range []string{f.Ecosystem, f.Dependency, f.Version, f.Generation} {
		if want != "" && fields[i] != want {
			return false
		}
	}
	return true
}

func joinKey(parts ...string) ([]byte, error) {
	for _, p := range parts {
		if strings.Contains(p, "\x00") {
			return nil, fmt.Errorf("embedded: metadata value %q contains a NUL byte", p)
		}
	}
	return []byte(strings.Join(parts, "\x00")), nil
}

// encodeValue stores what the key doesn't: source type, authority, and
// the vector, as little-endian binary.
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

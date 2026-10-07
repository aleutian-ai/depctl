// Package keyword implements backend.VectorBackend as keyword search
// (BM25) in a single bbolt file, so search needs no embedding model
// (LOCAL-001). Like the embedded vector store it scopes first and ranks
// second: a search is filtered to one dependency version (hundreds to a
// few thousand chunks), so BM25 is computed over exactly those chunks at
// query time and no inverted index is kept.
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
)

// Within a namespace's bucket: all points, and an index from a point's
// identity (generation, chunk ID) to its points key.
var (
	pointsKey = []byte("points")
	idsKey    = []byte("ids")
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
		if _, err := bk.CreateBucketIfNotExists(pointsKey); err != nil {
			return err
		}
		_, err = bk.CreateBucketIfNotExists(idsKey)
		return err
	})
}

// Upsert indexes req.Points' Text in one transaction.
func (s *Store) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		bk, err := namespace(tx, req.Namespace)
		if err != nil {
			return err
		}
		points, ids := bk.Bucket(pointsKey), bk.Bucket(idsKey)
		for _, p := range req.Points {
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
			if err := points.Put(key, encodeValue(m, Tokenize(p.Text))); err != nil {
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
		bk := tx.Bucket([]byte(req.Namespace))
		if bk == nil {
			return nil // never created: nothing to delete
		}
		points, ids := bk.Bucket(pointsKey), bk.Bucket(idsKey)
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
		return scan(bk.Bucket(pointsKey), req.Filter, func(f []string, v []byte) {
			m, length, tf := decodeValue(v, queryTerms)
			m.Ecosystem, m.Dependency, m.Version, m.Generation = f[0], f[1], f[2], f[3]
			docs = append(docs, doc{point: backend.ScoredPoint{ID: f[4], Metadata: m}, length: length, tf: tf})
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
		return scan(bk.Bucket(pointsKey), filter, func([]string, []byte) { n++ })
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

// scan calls fn with the key fields and value of every point f selects.
// Keys are ecosystem\0dependency\0version\0generation\0id, so the leading
// filter fields that are set become a prefix and the scan touches only
// those points.
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
			return nil, fmt.Errorf("keyword: metadata value %q contains a NUL byte", p)
		}
	}
	return []byte(strings.Join(parts, "\x00")), nil
}

// encodeValue stores source type, authority, the document's length in
// terms, and each distinct term with its count.
func encodeValue(m backend.PointMetadata, terms []string) []byte {
	tf := map[string]int{}
	for _, t := range terms {
		tf[t]++
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(m.SourceType)))
	out = append(out, m.SourceType...)
	out = binary.LittleEndian.AppendUint64(out, uint64(int64(m.Authority)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(terms)))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(tf)))
	for t, n := range tf {
		out = binary.LittleEndian.AppendUint16(out, uint16(len(t)))
		out = append(out, t...)
		out = binary.LittleEndian.AppendUint32(out, uint32(n))
	}
	return out
}

// decodeValue returns the metadata, document length, and the counts of
// just the terms in want (all a query needs).
func decodeValue(v []byte, want map[string]bool) (backend.PointMetadata, int, map[string]int) {
	n := int(binary.LittleEndian.Uint32(v))
	m := backend.PointMetadata{SourceType: string(v[4 : 4+n])}
	v = v[4+n:]
	m.Authority = int(int64(binary.LittleEndian.Uint64(v)))
	length := int(binary.LittleEndian.Uint32(v[8:]))
	distinct := int(binary.LittleEndian.Uint32(v[12:]))
	v = v[16:]
	tf := map[string]int{}
	for range distinct {
		l := int(binary.LittleEndian.Uint16(v))
		term := v[2 : 2+l]
		count := int(binary.LittleEndian.Uint32(v[2+l:]))
		if want[string(term)] {
			tf[string(term)] = count
		}
		v = v[6+l:]
	}
	return m, length, tf
}

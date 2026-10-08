package keyword

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/backend"
)

// writeLegacy builds a file in the v0.3.0 layout: a "points" bucket keyed
// ecosystem\0dependency\0version\0generation\0id with fixed-width values,
// and the "ids" index.
func writeLegacy(t *testing.T, path string, points map[string]string) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		ns, _ := tx.CreateBucketIfNotExists([]byte("depctl-old"))
		pb, _ := ns.CreateBucketIfNotExists([]byte("points"))
		ib, _ := ns.CreateBucketIfNotExists([]byte("ids"))
		for key, text := range points {
			terms := Tokenize(text)
			tf := map[string]int{}
			for _, term := range terms {
				tf[term]++
			}
			v := binary.LittleEndian.AppendUint32(nil, 3)
			v = append(v, "git"...)
			v = binary.LittleEndian.AppendUint64(v, 100)
			v = binary.LittleEndian.AppendUint32(v, uint32(len(terms)))
			v = binary.LittleEndian.AppendUint32(v, uint32(len(tf)))
			for term, n := range tf {
				v = binary.LittleEndian.AppendUint16(v, uint16(len(term)))
				v = append(v, term...)
				v = binary.LittleEndian.AppendUint32(v, uint32(n))
			}
			pb.Put([]byte(key), v)
			f := strings.Split(key, "\x00")
			ib.Put([]byte(f[3]+"\x00"+f[4]), []byte(key))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A v0.3.0 file is converted on first open: same answers, new layout,
// and no leftover temporary file.
func TestLegacyFileIsConvertedOnOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "keyword.db")
	writeLegacy(t, path, map[string]string{
		"go\x00github.com/google/uuid\x00v1.6.0\x00gen-6\x00new":       "New creates a new random UUID or panics.",
		"go\x00github.com/google/uuid\x00v1.6.0\x00gen-6\x00newrandom": "NewRandom returns a Random (Version 4) UUID.",
		"go\x00github.com/google/uuid\x00v1.5.0\x00gen-5\x00newrandom": "NewRandom returns a Random (Version 4) UUID.",
	})

	s := New(path)
	f := &backend.Filter{Dependency: "github.com/google/uuid", Version: "v1.6.0"}
	res, err := s.Query(ctx, backend.QueryRequest{Namespace: "depctl-old", Text: "NewRandom", TopK: 5, Filter: f})
	if err != nil {
		t.Fatalf("Query after conversion: %v", err)
	}
	if len(res.Points) == 0 || res.Points[0].ID != "newrandom" || res.Points[0].Metadata.SourceType != "git" || res.Points[0].Metadata.Authority != 100 {
		t.Errorf("Query(NewRandom, v1.6.0) = %+v, want NewRandom first, with its metadata", res.Points)
	}
	for _, p := range res.Points {
		if p.Metadata.Version != "v1.6.0" {
			t.Errorf("point %s from %s, want only v1.6.0", p.ID, p.Metadata.Version)
		}
	}
	if n, err := s.Count(ctx, "depctl-old", nil); err != nil || n != 3 {
		t.Errorf("Count after conversion = %d, %v; want 3", n, err)
	}

	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	db.View(func(tx *bolt.Tx) error {
		ns := tx.Bucket([]byte("depctl-old"))
		if ns.Bucket([]byte("points")) != nil || ns.Bucket([]byte("ids")) != nil {
			t.Error("legacy buckets survived the conversion")
		}
		if ns.Bucket(chunksKey).Bucket([]byte("gen-6")) == nil {
			t.Error("no per-generation bucket for gen-6 after conversion")
		}
		return nil
	})
	if _, err := os.Stat(path + ".migrating"); !os.IsNotExist(err) {
		t.Errorf("temporary conversion file left behind (stat err %v)", err)
	}
}

// A generation is one dependency version; a point claiming otherwise is
// refused rather than silently relabeling the generation's other points.
func TestUpsertRejectsAPointThatContradictsItsGeneration(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "keyword.db"))
	ns := backend.Namespace{Name: "depctl-test"}
	if err := s.EnsureNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	m := backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: "v1", Generation: "g"}
	if err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "a", Text: "alpha", Metadata: m}}}); err != nil {
		t.Fatal(err)
	}
	m.Version = "v2"
	err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "b", Text: "beta", Metadata: m}}})
	if err == nil || !strings.Contains(err.Error(), "one dependency version") {
		t.Fatalf("Upsert of a v2 point into a v1 generation = %v, want a clear error", err)
	}
}

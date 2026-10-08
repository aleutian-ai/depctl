package embedded

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

// writeLegacy builds a file in the v0.3.0 layout: "dims", a "points"
// bucket keyed ecosystem\0dependency\0version\0generation\0id, and the
// "ids" index.
func writeLegacy(t *testing.T, path string, points map[string][]float32) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		ns, _ := tx.CreateBucketIfNotExists([]byte("depctl-old"))
		ns.Put(dimsKey, binary.LittleEndian.AppendUint32(nil, 4))
		pb, _ := ns.CreateBucketIfNotExists([]byte("points"))
		ib, _ := ns.CreateBucketIfNotExists([]byte("ids"))
		for key, vec := range points {
			pb.Put([]byte(key), encodeValue(backend.PointMetadata{SourceType: "godoc", Authority: 90}, vec))
			f := strings.Split(key, "\x00")
			ib.Put([]byte(f[3]+"\x00"+f[4]), []byte(key))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A v0.3.0 file is converted on first open: same vectors and answers,
// new layout, nothing recomputed, no leftover temporary file.
func TestLegacyFileIsConvertedOnOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.db")
	writeLegacy(t, path, map[string][]float32{
		"go\x00d\x00v1\x00gen-1\x00near": {1, 0.1, 0, 0},
		"go\x00d\x00v1\x00gen-1\x00far":  {0, 0, 1, 0},
		"go\x00d\x00v2\x00gen-2\x00near": {1, 0, 0, 0},
	})

	s := New(path)
	res, err := s.Query(ctx, backend.QueryRequest{Namespace: "depctl-old", Vector: []float32{1, 0, 0, 0}, TopK: 5, Filter: &backend.Filter{Version: "v1"}})
	if err != nil {
		t.Fatalf("Query after conversion: %v", err)
	}
	if len(res.Points) != 2 || res.Points[0].ID != "near" || res.Points[0].Metadata.Generation != "gen-1" || res.Points[0].Metadata.SourceType != "godoc" || res.Points[0].Metadata.Authority != 90 {
		t.Errorf("Query(v1) = %+v, want near then far from gen-1, with metadata", res.Points)
	}
	if n, err := s.Count(ctx, "depctl-old", nil); err != nil || n != 3 {
		t.Errorf("Count after conversion = %d, %v; want 3", n, err)
	}
	if err := s.EnsureNamespace(ctx, backend.Namespace{Name: "depctl-old", Dimensions: 4, Distance: "cosine"}); err != nil {
		t.Errorf("the dimension didn't survive conversion: %v", err)
	}
	if _, err := os.Stat(path + ".migrating"); !os.IsNotExist(err) {
		t.Errorf("temporary conversion file left behind (stat err %v)", err)
	}
}

func TestUpsertRejectsAPointThatContradictsItsGeneration(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "vectors.db"))
	ns := backend.Namespace{Name: "depctl-test", Dimensions: 2, Distance: "cosine"}
	if err := s.EnsureNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	m := backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: "v1", Generation: "g"}
	if err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "a", Vector: []float32{1, 0}, Metadata: m}}}); err != nil {
		t.Fatal(err)
	}
	m.Version = "v2"
	err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "b", Vector: []float32{0, 1}, Metadata: m}}})
	if err == nil || !strings.Contains(err.Error(), "one dependency version") {
		t.Fatalf("Upsert of a v2 point into a v1 generation = %v, want a clear error", err)
	}
}

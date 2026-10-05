// Package conformance is the shared test suite every backend.VectorBackend
// adapter must pass (VEC-010). An adapter's own test calls Run with a
// constructor; nothing here knows which backend it's testing.
package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
)

const dims = 4

// Run exercises newBackend against the whole VectorBackend contract.
// Every subtest gets a fresh backend from newBackend and its own
// namespace, so one shared server (e.g. a single test container) can back
// the entire run.
func Run(t *testing.T, newBackend func(t *testing.T) backend.VectorBackend) {
	t.Run("Health", func(t *testing.T) {
		if err := newBackend(t).Health(context.Background()); err != nil {
			t.Fatalf("Health: %v", err)
		}
	})

	t.Run("CapabilitiesCoverWhatRagctlRequires", func(t *testing.T) {
		caps, err := newBackend(t).Capabilities(context.Background())
		if err != nil {
			t.Fatalf("Capabilities: %v", err)
		}
		// Version-correct retrieval is impossible without both.
		if !caps.VectorSearch || !caps.MetadataFilter {
			t.Errorf("Capabilities = %+v; ragctl requires VectorSearch and MetadataFilter", caps)
		}
	})

	t.Run("EnsureNamespaceIsIdempotent", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		if err := b.EnsureNamespace(context.Background(), ns); err != nil {
			t.Fatalf("second EnsureNamespace: %v", err)
		}
	})

	t.Run("UpsertThenQueryRoundTripsIDsAndMetadata", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		want := point("chunk-a", [4]float32{1, 0, 0, 0}, "v1", "gen-1")
		want.Metadata.SourceType, want.Metadata.Authority = "godoc", 90
		upsert(t, b, ns, want, point("chunk-b", [4]float32{0, 1, 0, 0}, "v1", "gen-1"))

		res := query(t, b, ns, [4]float32{1, 0, 0, 0}, 1, nil)
		if len(res) != 1 {
			t.Fatalf("Query returned %d points, want 1", len(res))
		}
		got := res[0]
		// Callers' IDs must come back as-is, even if the backend stores a
		// derived ID internally (Qdrant requires UUIDs).
		if got.ID != want.ID {
			t.Errorf("ID = %q, want %q", got.ID, want.ID)
		}
		if got.Metadata != want.Metadata {
			t.Errorf("Metadata = %+v, want %+v", got.Metadata, want.Metadata)
		}
	})

	t.Run("QueryRanksBySimilarityAndRespectsTopK", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		upsert(t, b, ns,
			point("near", [4]float32{1, 0.1, 0, 0}, "v1", "gen-1"),
			point("mid", [4]float32{1, 1, 0, 0}, "v1", "gen-1"),
			point("far", [4]float32{0, 0, 1, 0}, "v1", "gen-1"),
		)
		res := query(t, b, ns, [4]float32{1, 0, 0, 0}, 2, nil)
		if ids := idsOf(res); len(ids) != 2 || ids[0] != "near" || ids[1] != "mid" {
			t.Errorf("Query ranking = %v, want [near mid]", ids)
		}
		if res[0].Score < res[1].Score {
			t.Errorf("scores not descending: %v then %v", res[0].Score, res[1].Score)
		}
	})

	t.Run("UpsertSameIDDoesNotDuplicate", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		upsert(t, b, ns, point("chunk-a", [4]float32{1, 0, 0, 0}, "v1", "gen-1"))
		updated := point("chunk-a", [4]float32{1, 0, 0, 0}, "v1", "gen-1")
		updated.Metadata.Authority = 7
		upsert(t, b, ns, updated)

		if n := count(t, b, ns, nil); n != 1 {
			t.Fatalf("Count after re-upserting the same ID = %d, want 1", n)
		}
		if res := query(t, b, ns, [4]float32{1, 0, 0, 0}, 5, nil); len(res) != 1 || res[0].Metadata.Authority != 7 {
			t.Errorf("Query after re-upsert = %+v, want one point with the updated metadata", res)
		}
	})

	t.Run("MetadataFilterConstrainsQueryToExactVersion", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		other := point("other-pkg", [4]float32{1, 0, 0, 0}, "v2", "gen-x")
		other.Metadata.Dependency = "example.com/other"
		upsert(t, b, ns,
			point("v1-a", [4]float32{1, 0, 0, 0}, "v1", "gen-1"),
			point("v2-a", [4]float32{1, 0, 0, 0}, "v2", "gen-2"),
			point("v2-b", [4]float32{0.9, 0.1, 0, 0}, "v2", "gen-2"),
			other,
		)
		f := &backend.Filter{Ecosystem: "go", Dependency: "example.com/dep", Version: "v2"}
		if ids := idsOf(query(t, b, ns, [4]float32{1, 0, 0, 0}, 10, f)); !sameSet(ids, "v2-a", "v2-b") {
			t.Errorf("version-filtered Query = %v, want only [v2-a v2-b]", ids)
		}
		none := &backend.Filter{Dependency: "example.com/dep", Version: "v9"}
		if res := query(t, b, ns, [4]float32{1, 0, 0, 0}, 10, none); len(res) != 0 {
			t.Errorf("Query with a filter matching nothing = %v, want none", idsOf(res))
		}
	})

	// A backend that tokenizes text for filtering (Weaviate's default) can
	// match "v1.5.0" against "v1.0.5": same tokens, different version.
	t.Run("FilterMatchesWholeValuesNotTokens", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		swapped := point("swapped", [4]float32{1, 0, 0, 0}, "v1.0.5", "gen-2")
		swapped.Metadata.Dependency = "example.com/dep/sub"
		upsert(t, b, ns, point("exact", [4]float32{1, 0, 0, 0}, "v1.5.0", "gen-1"), swapped)
		f := &backend.Filter{Dependency: "example.com/dep", Version: "v1.5.0"}
		if ids := idsOf(query(t, b, ns, [4]float32{1, 0, 0, 0}, 10, f)); !sameSet(ids, "exact") {
			t.Errorf("Query(dependency example.com/dep, version v1.5.0) = %v, want only [exact]", ids)
		}
		if n := count(t, b, ns, &backend.Filter{Version: "v1.0"}); n != 0 {
			t.Errorf("Count(version v1.0) = %d, want 0: a prefix is not a match", n)
		}
	})

	t.Run("GenerationFilterSeparatesBuildsOfTheSameVersion", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		upsert(t, b, ns,
			point("old-build", [4]float32{1, 0, 0, 0}, "v1", "gen-old"),
			point("new-build", [4]float32{1, 0, 0, 0}, "v1", "gen-new"),
		)
		f := &backend.Filter{Version: "v1", Generation: "gen-new"}
		if ids := idsOf(query(t, b, ns, [4]float32{1, 0, 0, 0}, 10, f)); !sameSet(ids, "new-build") {
			t.Errorf("generation-filtered Query = %v, want only [new-build]", ids)
		}
	})

	// Content reuse (GEN-003) gives an unchanged chunk the same ID in
	// every version that has it. If one version's upsert overwrote the
	// other's point, the older version's search silently loses those
	// chunks: found end-to-end on pgvector, which first keyed on ID alone.
	t.Run("SameIDInTwoGenerationsIsTwoPoints", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		upsert(t, b, ns,
			point("shared", [4]float32{1, 0, 0, 0}, "v1", "gen-1"),
			point("shared", [4]float32{1, 0, 0, 0}, "v2", "gen-2"),
		)
		if n := count(t, b, ns, nil); n != 2 {
			t.Fatalf("Count after upserting one ID under two generations = %d, want 2", n)
		}
		for _, v := range []string{"v1", "v2"} {
			res := query(t, b, ns, [4]float32{1, 0, 0, 0}, 10, &backend.Filter{Version: v})
			if len(res) != 1 || res[0].ID != "shared" || res[0].Metadata.Version != v {
				t.Errorf("Query(version %s) = %+v, want the one \"shared\" point labelled %s", v, res, v)
			}
		}
		del(t, b, backend.DeleteRequest{Namespace: ns.Name, IDs: []string{"shared"}})
		if n := count(t, b, ns, nil); n != 0 {
			t.Errorf("Count after deleting ID \"shared\" = %d, want 0: delete by ID covers every generation", n)
		}
	})

	t.Run("CountIsExact", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		upsert(t, b, ns,
			point("a", [4]float32{1, 0, 0, 0}, "v1", "gen-1"),
			point("b", [4]float32{0, 1, 0, 0}, "v1", "gen-1"),
			point("c", [4]float32{0, 0, 1, 0}, "v2", "gen-2"),
		)
		if n := count(t, b, ns, nil); n != 3 {
			t.Errorf("Count(nil) = %d, want 3", n)
		}
		if n := count(t, b, ns, &backend.Filter{Version: "v1"}); n != 2 {
			t.Errorf("Count(version v1) = %d, want 2", n)
		}
		if n := count(t, b, ns, &backend.Filter{Generation: "gen-none"}); n != 0 {
			t.Errorf("Count(no match) = %d, want 0", n)
		}
	})

	t.Run("DeleteByIDs", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		upsert(t, b, ns,
			point("keep", [4]float32{1, 0, 0, 0}, "v1", "gen-1"),
			point("drop", [4]float32{0, 1, 0, 0}, "v1", "gen-1"),
		)
		del(t, b, backend.DeleteRequest{Namespace: ns.Name, IDs: []string{"drop"}})
		if ids := idsOf(query(t, b, ns, [4]float32{1, 1, 0, 0}, 10, nil)); !sameSet(ids, "keep") {
			t.Errorf("after deleting [drop]: %v, want only [keep]", ids)
		}
	})

	t.Run("DeleteByFilterMatchesEveryFieldNotAny", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		if !deleteByFilter(t, b) {
			return
		}
		otherDep := point("other-dep-v1", [4]float32{0, 0, 1, 0}, "v1", "gen-9")
		otherDep.Metadata.Dependency = "example.com/other"
		upsert(t, b, ns,
			point("dep-v1", [4]float32{1, 0, 0, 0}, "v1", "gen-1"),
			point("dep-v2", [4]float32{0, 1, 0, 0}, "v2", "gen-2"),
			otherDep,
		)
		// Dependency AND Version: must not also delete other packages'
		// v1 or this package's v2 (epic 14 found a Qdrant delete that
		// matched ANY field).
		del(t, b, backend.DeleteRequest{Namespace: ns.Name, Filter: &backend.Filter{Dependency: "example.com/dep", Version: "v1"}})
		if ids := idsOf(query(t, b, ns, [4]float32{1, 1, 1, 0}, 10, nil)); !sameSet(ids, "dep-v2", "other-dep-v1") {
			t.Errorf("after deleting example.com/dep@v1: %v, want [dep-v2 other-dep-v1]", ids)
		}
	})

	t.Run("DeleteByIDsAndFilterIsAUnion", func(t *testing.T) {
		b, ns := setup(t, newBackend)
		if !deleteByFilter(t, b) {
			return
		}
		upsert(t, b, ns,
			point("by-id", [4]float32{1, 0, 0, 0}, "v1", "gen-1"),
			point("by-filter", [4]float32{0, 1, 0, 0}, "v2", "gen-2"),
			point("keep", [4]float32{0, 0, 1, 0}, "v3", "gen-3"),
		)
		del(t, b, backend.DeleteRequest{Namespace: ns.Name, IDs: []string{"by-id"}, Filter: &backend.Filter{Version: "v2"}})
		if ids := idsOf(query(t, b, ns, [4]float32{1, 1, 1, 0}, 10, nil)); !sameSet(ids, "keep") {
			t.Errorf("after deleting by-id plus version v2: %v, want only [keep]", ids)
		}
	})

	t.Run("NamespacesAreIsolated", func(t *testing.T) {
		b, nsA := setup(t, newBackend)
		nsB := newNamespace(t)
		if err := b.EnsureNamespace(context.Background(), nsB); err != nil {
			t.Fatalf("EnsureNamespace B: %v", err)
		}
		upsert(t, b, nsA, point("in-a", [4]float32{1, 0, 0, 0}, "v1", "gen-1"))
		upsert(t, b, nsB, point("in-b", [4]float32{1, 0, 0, 0}, "v1", "gen-1"))

		if ids := idsOf(query(t, b, nsA, [4]float32{1, 0, 0, 0}, 10, nil)); !sameSet(ids, "in-a") {
			t.Errorf("Query in A = %v, want only [in-a]", ids)
		}
		if deleteByFilterCap(t, b) {
			del(t, b, backend.DeleteRequest{Namespace: nsA.Name, Filter: &backend.Filter{Version: "v1"}})
			if n := count(t, b, nsB, nil); n != 1 {
				t.Errorf("Count in B after a filtered delete in A = %d, want 1", n)
			}
		}
	})
}

func setup(t *testing.T, newBackend func(t *testing.T) backend.VectorBackend) (backend.VectorBackend, backend.Namespace) {
	t.Helper()
	b := newBackend(t)
	ns := newNamespace(t)
	if err := b.EnsureNamespace(context.Background(), ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	return b, ns
}

func newNamespace(t *testing.T) backend.Namespace {
	t.Helper()
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return backend.Namespace{Name: "conformance_" + hex.EncodeToString(buf[:]), Dimensions: dims, Distance: "cosine"}
}

func point(id string, vec [4]float32, version, generation string) backend.Point {
	return backend.Point{
		ID:     id,
		Vector: vec[:],
		Metadata: backend.PointMetadata{
			Ecosystem: "go", Dependency: "example.com/dep", Version: version,
			Generation: generation, SourceType: "git", Authority: 100,
		},
	}
}

func upsert(t *testing.T, b backend.VectorBackend, ns backend.Namespace, points ...backend.Point) {
	t.Helper()
	if err := b.Upsert(context.Background(), backend.UpsertRequest{Namespace: ns.Name, Points: points}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func query(t *testing.T, b backend.VectorBackend, ns backend.Namespace, vec [4]float32, topK int, f *backend.Filter) []backend.ScoredPoint {
	t.Helper()
	res, err := b.Query(context.Background(), backend.QueryRequest{Namespace: ns.Name, Vector: vec[:], TopK: topK, Filter: f})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return res.Points
}

func count(t *testing.T, b backend.VectorBackend, ns backend.Namespace, f *backend.Filter) int {
	t.Helper()
	n, err := b.Count(context.Background(), ns.Name, f)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	return n
}

func del(t *testing.T, b backend.VectorBackend, req backend.DeleteRequest) {
	t.Helper()
	if err := b.Delete(context.Background(), req); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// deleteByFilter skips the calling subtest when the backend can't delete
// by filter (the ticket's capability-gated skip) and reports whether it
// can.
func deleteByFilter(t *testing.T, b backend.VectorBackend) bool {
	t.Helper()
	if !deleteByFilterCap(t, b) {
		t.Skip("backend does not support DeleteByFilter")
		return false
	}
	return true
}

func deleteByFilterCap(t *testing.T, b backend.VectorBackend) bool {
	t.Helper()
	caps, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	return caps.DeleteByFilter
}

func idsOf(points []backend.ScoredPoint) []string {
	ids := make([]string, len(points))
	for i, p := range points {
		ids[i] = p.ID
	}
	return ids
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

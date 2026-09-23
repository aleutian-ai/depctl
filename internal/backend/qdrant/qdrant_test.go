package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
)

func TestEnsureNamespaceCreatesCollectionWhenMissing(t *testing.T) {
	var gotCreate createCollectionRequest
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			putCalled = true
			json.NewDecoder(r.Body).Decode(&gotCreate)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.EnsureNamespace(context.Background(), backend.Namespace{Name: "ragctl", Dimensions: 768, Distance: "cosine"})
	if err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	if !putCalled {
		t.Fatal("PUT /collections/ragctl was never called")
	}
	if gotCreate.Vectors.Size != 768 || gotCreate.Vectors.Distance != "Cosine" {
		t.Errorf("create request = %+v, want size=768 distance=Cosine", gotCreate.Vectors)
	}
}

func TestEnsureNamespaceIsNoOpWhenCollectionExists(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			putCalled = true
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.EnsureNamespace(context.Background(), backend.Namespace{Name: "ragctl", Dimensions: 768}); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	if putCalled {
		t.Error("PUT was called even though the collection already exists")
	}
}

func TestUpsertSendsPointsWithPayload(t *testing.T) {
	var got upsertRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/ragctl/points" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Upsert(context.Background(), backend.UpsertRequest{
		Namespace: "ragctl",
		Points: []backend.Point{
			{ID: "chk_abc", Vector: []float32{1, 2}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "grpc-go", Version: "v1.67.0", Generation: "gen_1", Authority: 100}},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(got.Points) != 1 {
		t.Fatalf("got %d points, want 1", len(got.Points))
	}
	p := got.Points[0]
	if p.ID == "chk_abc" {
		t.Error("point ID sent to Qdrant should be a derived UUID, not the raw chunk ID")
	}
	if p.Payload["_id"] != "chk_abc" {
		t.Errorf("payload._id = %v, want chk_abc", p.Payload["_id"])
	}
	if p.Payload["version"] != "v1.67.0" {
		t.Errorf("payload.version = %v, want v1.67.0", p.Payload["version"])
	}
}

// TestUpsertGivesEachGenerationItsOwnPointForIdenticalContent is the
// regression test for the STRESS-005/COORD-003 finding: chunk IDs are
// content-derived, so two generations holding byte-identical content
// (sibling modules of one monorepo, or an unchanged file across two
// versions) produced the same Qdrant point ID, and the later upsert
// silently overwrote the earlier generation's payload — leaving it with
// zero searchable points despite a "successful" sync. Each generation
// must own its own point.
func TestUpsertGivesEachGenerationItsOwnPointForIdenticalContent(t *testing.T) {
	var got upsertRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Upsert(context.Background(), backend.UpsertRequest{
		Namespace: "ragctl",
		Points: []backend.Point{
			{ID: "chk_same", Vector: []float32{1}, Metadata: backend.PointMetadata{Dependency: "billing", Generation: "gen_A"}},
			{ID: "chk_same", Vector: []float32{1}, Metadata: backend.PointMetadata{Dependency: "workflows", Generation: "gen_B"}},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(got.Points) != 2 {
		t.Fatalf("got %d points, want 2", len(got.Points))
	}
	if got.Points[0].ID == got.Points[1].ID {
		t.Errorf("both generations were assigned point ID %s — the second upsert would overwrite the first's payload", got.Points[0].ID)
	}
}

func TestUpsertBatchesRequests(t *testing.T) {
	var requestSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req upsertRequest
		json.NewDecoder(r.Body).Decode(&req)
		requestSizes = append(requestSizes, len(req.Points))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithBatchSize(2))
	points := make([]backend.Point, 5)
	for i := range points {
		points[i] = backend.Point{ID: "chk_" + string(rune('a'+i)), Vector: []float32{1}}
	}
	if err := c.Upsert(context.Background(), backend.UpsertRequest{Namespace: "ragctl", Points: points}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	want := []int{2, 2, 1}
	if len(requestSizes) != len(want) {
		t.Fatalf("request sizes = %v, want %v", requestSizes, want)
	}
	for i, w := range want {
		if requestSizes[i] != w {
			t.Errorf("request %d size = %d, want %d", i, requestSizes[i], w)
		}
	}
}

func TestQueryAppliesFilterAndParsesResults(t *testing.T) {
	var gotReq searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotReq)
		json.NewEncoder(w).Encode(searchResponse{
			Result: []searchResultItem{
				{ID: "uuid-1", Score: 0.9, Payload: map[string]any{"_id": "chk_abc", "ecosystem": "go", "dependency": "grpc-go", "version": "v1.67.0", "authority": float64(100)}},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	result, err := c.Query(context.Background(), backend.QueryRequest{
		Namespace: "ragctl",
		Vector:    []float32{1, 2, 3},
		TopK:      5,
		Filter:    &backend.Filter{Version: "v1.67.0"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if gotReq.Limit != 5 {
		t.Errorf("request limit = %d, want 5", gotReq.Limit)
	}
	if gotReq.Filter == nil || len(gotReq.Filter.Must) != 1 || gotReq.Filter.Must[0].Key != "version" {
		t.Errorf("request filter = %+v, want a single version match", gotReq.Filter)
	}

	if len(result.Points) != 1 {
		t.Fatalf("got %d points, want 1", len(result.Points))
	}
	got := result.Points[0]
	if got.ID != "chk_abc" {
		t.Errorf("ID = %s, want chk_abc (original ragctl ID, not the Qdrant UUID)", got.ID)
	}
	if got.Score != 0.9 {
		t.Errorf("Score = %v, want 0.9", got.Score)
	}
	if got.Metadata.Authority != 100 {
		t.Errorf("Metadata.Authority = %d, want 100", got.Metadata.Authority)
	}
}

// TestDeleteByIDsAndFilterSendsTwoSeparateRequests is a regression test:
// Qdrant's points_delete selector is a "one of {points, filter}", and
// empirically (verified against a real server) combining both into one
// request body silently drops the filter. Delete must send IDs and
// Filter as two independent requests rather than one combined selector.
func TestDeleteByIDsAndFilterSendsTwoSeparateRequests(t *testing.T) {
	var got []pointsSelector
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sel pointsSelector
		json.NewDecoder(r.Body).Decode(&sel)
		got = append(got, sel)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Delete(context.Background(), backend.DeleteRequest{
		Namespace: "ragctl",
		IDs:       []string{"chk_abc"},
		Filter:    &backend.Filter{Generation: "gen_old"},
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2 (one for IDs, one for filter)", len(got))
	}
	if got[0].Filter == nil || got[0].Filter.Must[0].Key != "_id" || len(got[0].Points) != 0 {
		t.Errorf("first request = %+v, want a filter on the chunk's _id payload, no explicit points", got[0])
	}
	if got[1].Filter == nil || got[1].Filter.Must[0].Key != "generation" || len(got[1].Points) != 0 {
		t.Errorf("second request = %+v, want filter only, no points", got[1])
	}
}

func TestDeleteWithOnlyIDsSendsOneRequest(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Delete(context.Background(), backend.DeleteRequest{Namespace: "ragctl", IDs: []string{"chk_abc"}}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func Test5xxIsBackendUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("Health succeeded, want error")
	}
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("Health error = %v, want wrapping ErrBackendUnavailable", err)
	}
}

func Test4xxIsBackendRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.Upsert(context.Background(), backend.UpsertRequest{Namespace: "ragctl", Points: []backend.Point{{ID: "chk_1"}}})
	if err == nil {
		t.Fatal("Upsert succeeded, want error")
	}
	if !errors.Is(err, ErrBackendRequest) {
		t.Errorf("Upsert error = %v, want wrapping ErrBackendRequest", err)
	}
}

func TestHealthCollectionSucceedsWhenCollectionExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/ragctl" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.HealthCollection(context.Background(), "ragctl"); err != nil {
		t.Fatalf("HealthCollection: %v", err)
	}
}

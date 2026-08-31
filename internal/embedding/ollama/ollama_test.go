package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbedRequestShapeAndResponseParsing(t *testing.T) {
	var gotReq embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s, want /api/embed", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		json.NewEncoder(w).Encode(embedResponse{
			Embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "nomic-embed-text")
	vecs, err := c.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if gotReq.Model != "nomic-embed-text" {
		t.Errorf("request model = %s, want nomic-embed-text", gotReq.Model)
	}
	if len(gotReq.Input) != 2 || gotReq.Input[0] != "hello" || gotReq.Input[1] != "world" {
		t.Errorf("request input = %v, want [hello world]", gotReq.Input)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][1] != 0.4 {
		t.Errorf("Embed result = %v", vecs)
	}
}

func TestEmbedRetriesTransientFailureThenSucceeds(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{1, 2, 3}}})
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	vecs, err := c.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("attempts = %d, want 2 (one failure, one success)", attempts)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Errorf("Embed result = %v", vecs)
	}
}

func TestEmbedNonTransientErrorDoesNotRetry(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("Embed succeeded, want error")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", attempts)
	}
}

func TestEmbedContextCancellationAbortsInFlightCall(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	}))
	defer srv.Close()
	defer close(unblock)

	c := New(srv.URL, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.Embed(ctx, []string{"x"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Embed returned nil error after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Embed did not return after context cancellation")
	}
}

func TestDimensionsProbesOnceAndCaches(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{1, 2, 3, 4}}})
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	d1, err := c.Dimensions(context.Background())
	if err != nil {
		t.Fatalf("Dimensions: %v", err)
	}
	d2, err := c.Dimensions(context.Background())
	if err != nil {
		t.Fatalf("Dimensions (second call): %v", err)
	}
	if d1 != 4 || d2 != 4 {
		t.Errorf("Dimensions = %d, %d, want 4, 4", d1, d2)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("probe attempts = %d, want 1 (cached after first call)", attempts)
	}
}

func TestEmbedBatchesRequests(t *testing.T) {
	var requestSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		json.NewDecoder(r.Body).Decode(&req)
		requestSizes = append(requestSizes, len(req.Input))
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{1}
		}
		json.NewEncoder(w).Encode(embedResponse{Embeddings: vecs})
	}))
	defer srv.Close()

	c := New(srv.URL, "m", WithBatchSize(2))
	texts := []string{"a", "b", "c", "d", "e"}
	vecs, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Errorf("got %d vectors, want %d", len(vecs), len(texts))
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

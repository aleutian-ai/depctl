package cognee

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddSendsMultipartFormWithDatasetAndFile(t *testing.T) {
	var gotPath, gotDatasetName, gotFileContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotDatasetName = r.FormValue("datasetName")
		file, _, err := r.FormFile("data")
		if err != nil {
			t.Fatalf("FormFile(data): %v", err)
		}
		defer file.Close()
		buf := make([]byte, 1024)
		n, _ := file.Read(buf)
		gotFileContent = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.Add(context.Background(), "proj-1", "google.golang.org-protobuf.json", []byte(`{"dependency":"google.golang.org/protobuf"}`)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if gotPath != "/api/v1/add" {
		t.Errorf("path = %q, want /api/v1/add", gotPath)
	}
	if gotDatasetName != "proj-1" {
		t.Errorf("datasetName = %q, want proj-1", gotDatasetName)
	}
	if gotFileContent != `{"dependency":"google.golang.org/protobuf"}` {
		t.Errorf("file content = %q, want the original JSON content", gotFileContent)
	}
}

func TestCognifySendsExactRequestShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody cognifyRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	if err := c.Cognify(context.Background(), "proj-1", 1024); err != nil {
		t.Fatalf("Cognify: %v", err)
	}

	if gotPath != "/api/v1/cognify" {
		t.Errorf("path = %q, want /api/v1/cognify", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
	if len(gotBody.Datasets) != 1 || gotBody.Datasets[0] != "proj-1" {
		t.Errorf("Datasets = %+v, want [proj-1]", gotBody.Datasets)
	}
	if gotBody.ChunkSize != 1024 {
		t.Errorf("ChunkSize = %d, want 1024", gotBody.ChunkSize)
	}
}

func TestAddAndCognifyReturnErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.Add(context.Background(), "proj-1", "f.json", []byte("{}")); err == nil {
		t.Error("Add: expected an error for a 500 response, got nil")
	}
	if err := c.Cognify(context.Background(), "proj-1", 0); err == nil {
		t.Error("Cognify: expected an error for a 500 response, got nil")
	}
}

func TestHealthUsesRealDatasetsEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotPath != "/api/v1/datasets" {
		t.Errorf("path = %q, want /api/v1/datasets", gotPath)
	}
}

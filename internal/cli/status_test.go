package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
)

// statusTestStores opens fresh stores and also returns their on-disk
// paths, which buildStatus measures.
func statusTestStores(t *testing.T) (*bboltstore.Store, *badgerstore.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control.db")
	badgerPath := filepath.Join(dir, "badger")

	store, err := bboltstore.Open(controlPath)
	if err != nil {
		t.Fatalf("bboltstore.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	badgerStore, err := badgerstore.Open(badgerPath)
	if err != nil {
		t.Fatalf("badgerstore.Open: %v", err)
	}
	t.Cleanup(func() { badgerStore.Close() })

	return store, badgerStore, controlPath, badgerPath
}

// deadBackendURL returns a URL nothing is listening on.
func deadBackendURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

func healthyBackendURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestStatusJSONRoundTrip(t *testing.T) {
	last := time.Date(2026, 9, 10, 20, 13, 25, 0, time.UTC)
	want := Status{
		Projects:             2,
		DependencyReferences: 57,
		ActiveGenerations:    20,
		Jobs:                 JobStats{Pending: 1, Running: 2, Failed: 3},
		StorageBboltBytes:    4096,
		StorageBadgerBytes:   1 << 20,
		Backend:              BackendStatus{Name: "qdrant", Healthy: true},
		LastSync:             &last,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Status
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}

	var keys map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatalf("Unmarshal into map: %v", err)
	}
	for _, k := range []string{"projects", "dependency_references", "active_generations", "jobs", "storage_bbolt_bytes", "storage_badger_bytes", "backend", "last_sync"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("JSON is missing key %q: %s", k, data)
		}
	}
}

func TestBuildStatusReflectsFixtureState(t *testing.T) {
	ctx := context.Background()
	store, badgerStore, controlPath, badgerPath := statusTestStores(t)

	for _, id := range []string{"proj_a", "proj_b"} {
		if err := store.PutProject(ctx, domain.Project{ID: id, Root: "/src/" + id}); err != nil {
			t.Fatalf("PutProject %s: %v", id, err)
		}
	}
	for _, pkg := range []string{"pkg-a", "pkg-b", "pkg-c"} {
		ref := domain.VersionReference{ProjectID: "proj_a", Ecosystem: domain.EcosystemNode, Package: pkg, Version: "1.0.0", Reason: domain.ReferenceReasonProject}
		if err := store.AddReference(ctx, ref); err != nil {
			t.Fatalf("AddReference %s: %v", pkg, err)
		}
	}
	seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, "pkg-a", "1.0.0", 5)
	newestID := seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, "pkg-b", "1.0.0", 5)

	otherBackend := domain.Generation{ID: "gen_other", Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "pkg-c"}, Version: "1.0.0"}}
	if err := store.PromoteGeneration(ctx, otherBackend, "some-other-backend"); err != nil {
		t.Fatalf("PromoteGeneration other backend: %v", err)
	}

	for i, state := range []domain.JobState{domain.JobPending, domain.JobRetry, domain.JobRunning, domain.JobFailed, domain.JobSucceeded} {
		if err := store.PutJob(ctx, domain.Job{ID: "job_" + string(rune('a'+i)), Type: "gc", State: state}); err != nil {
			t.Fatalf("PutJob: %v", err)
		}
	}

	st, err := buildStatus(ctx, store, "qdrant", controlPath, badgerPath)
	if err != nil {
		t.Fatalf("buildStatus: %v", err)
	}

	if st.Projects != 2 || st.DependencyReferences != 3 || st.ActiveGenerations != 2 {
		t.Errorf("counts = projects %d, references %d, active %d; want 2, 3, 2", st.Projects, st.DependencyReferences, st.ActiveGenerations)
	}
	if want := (JobStats{Pending: 2, Running: 1, Failed: 1}); st.Jobs != want {
		t.Errorf("Jobs = %+v, want %+v", st.Jobs, want)
	}
	if st.StorageBboltBytes <= 0 || st.StorageBadgerBytes <= 0 {
		t.Errorf("storage = bbolt %d, badger %d; want both > 0", st.StorageBboltBytes, st.StorageBadgerBytes)
	}

	newest, err := store.GetGeneration(ctx, newestID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if st.LastSync == nil || !st.LastSync.Equal(newest.UpdatedAt) {
		t.Errorf("LastSync = %v, want %v (newest promotion)", st.LastSync, newest.UpdatedAt)
	}
}

func TestBuildStatusEmptyStoreHasNoLastSync(t *testing.T) {
	store, _, controlPath, badgerPath := statusTestStores(t)
	st, err := buildStatus(context.Background(), store, "qdrant", controlPath, badgerPath)
	if err != nil {
		t.Fatalf("buildStatus: %v", err)
	}
	if st.LastSync != nil || st.ActiveGenerations != 0 {
		t.Errorf("empty store: LastSync = %v, ActiveGenerations = %d; want nil, 0", st.LastSync, st.ActiveGenerations)
	}
}

func TestProbeBackend(t *testing.T) {
	cfg := config.Default(t.TempDir())

	cfg.Vector.Endpoint = healthyBackendURL(t)
	if err := probeBackend(context.Background(), cfg); err != nil {
		t.Errorf("healthy backend: probeBackend = %v, want nil", err)
	}

	cfg.Vector.Endpoint = deadBackendURL(t)
	if err := probeBackend(context.Background(), cfg); err == nil {
		t.Error("dead backend: probeBackend = nil, want error")
	}

	cfg.Vector.Backend = "milvus"
	if err := probeBackend(context.Background(), cfg); err == nil {
		t.Error("unsupported backend: probeBackend = nil, want error")
	}
}

// writeTestConfig saves cfg at the isolated default config path and
// creates the data dir, as `ragctl init` would.
func writeTestConfig(t *testing.T, mutate func(*config.Config)) {
	t.Helper()
	dataDir, err := config.DefaultDataDir()
	if err != nil {
		t.Fatalf("DefaultDataDir: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll data dir: %v", err)
	}
	cfg := config.Default(dataDir)
	mutate(&cfg)

	path, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll config dir: %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save config: %v", err)
	}
}

func TestStatusCommandReportsDownBackendWithoutFailing(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)
	endpoint := deadBackendURL(t)
	writeTestConfig(t, func(c *config.Config) { c.Vector.Endpoint = endpoint })

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("status with backend down returned error: %v", err)
	}

	var st Status
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("output is not a Status: %v\n%s", err, out.String())
	}
	if st.Backend.Name != "qdrant" || st.Backend.Healthy {
		t.Errorf("Backend = %+v, want qdrant unhealthy", st.Backend)
	}
}

func TestStatusTextOutput(t *testing.T) {
	var out bytes.Buffer
	printStatusText(&out, Status{Projects: 1, Backend: BackendStatus{Name: "qdrant"}})
	text := out.String()
	for _, want := range []string{"projects:", "qdrant unhealthy", "last sync:", "never"} {
		if !bytes.Contains([]byte(text), []byte(want)) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"}
	for n, want := range cases {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

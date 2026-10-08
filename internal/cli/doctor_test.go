package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/config"
	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/domain"
)

const testEmbeddingModel = "nomic-embed-text"

// healthyOllamaURL returns a fake Ollama server that always reports
// testEmbeddingModel already pulled, so tests never depend on a real
// Ollama actually running on the machine they execute on.
func healthyOllamaURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{{"name": testEmbeddingModel + ":latest"}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// healthyDoctorEnv builds an env where every check passes: one active
// generation with its manifest and a complete replica embedded with the
// configured model, a reachable vector backend, a reachable embedding
// backend with the configured model already pulled, a clean registry, one
// scanned Go project, and git/go on a fake PATH. embeddingReadiness/
// vectorReadiness are left nil, so checkEmbeddingBackend/checkVectorBackend
// take the no-daemon synchronous-probe path, same as runDoctorDirect.
func healthyDoctorEnv(t *testing.T) *doctorEnv {
	t.Helper()
	ctx := context.Background()
	store, badgerStore, _, _ := statusTestStores(t)

	genID := seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, "pkg-a", "1.0.0", 5)
	replica := domain.BackendReplica{GenerationID: genID, BackendName: "qdrant", EmbeddingModel: testEmbeddingModel, Status: "complete", PointCount: 5, UpdatedAt: time.Now()}
	if err := store.PutBackendReplica(ctx, replica); err != nil {
		t.Fatalf("PutBackendReplica: %v", err)
	}

	if err := store.PutProject(ctx, domain.Project{ID: "proj_go", Root: "/src/go"}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if err := store.PutResolution(ctx, "proj_go", domain.Resolution{Ecosystem: domain.EcosystemGo}); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}

	cfg := config.Default(t.TempDir())
	cfg.Retrieval.Mode = config.RetrievalVector // a vector-only install: its points live in the (fake) Qdrant
	cfg.Vector.QdrantDefaults()
	cfg.Vector.Endpoint = healthyBackendURL(t)
	cfg.Embedding.Model = testEmbeddingModel
	// An install from before embedding prompts existed: plain text, full size.
	cfg.Embedding.QueryPrompt, cfg.Embedding.DocumentPrompt, cfg.Embedding.Dimensions = "", "", 0
	cfg.Embedding.Endpoint = healthyOllamaURL(t)

	return &doctorEnv{
		cfg:      cfg,
		store:    store,
		badger:   badgerStore,
		registry: describeTestRegistry(t, "pkg-a"),
		lookPath: func(file string) (string, error) { return "/usr/bin/" + file, nil },
		now:      time.Now(),
	}
}

func resultNamed(t *testing.T, results []CheckResult, name string) CheckResult {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no check named %q in %+v", name, results)
	return CheckResult{}
}

func TestDoctorHealthyFixtureIsAllOK(t *testing.T) {
	results := runChecks(context.Background(), healthyDoctorEnv(t))
	if len(results) != len(doctorChecks) {
		t.Fatalf("got %d results, want %d", len(results), len(doctorChecks))
	}
	for _, r := range results {
		if r.Severity != SeverityOK {
			t.Errorf("%s = %s (%s), want OK", r.Name, r.Severity, r.Detail)
		}
	}
	if got := worstSeverity(results); got != SeverityOK {
		t.Errorf("worstSeverity = %s, want OK", got)
	}
}

func TestDoctorMissingGitIsUnhealthy(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.lookPath = func(file string) (string, error) {
		if file == "git" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + file, nil
	}

	results := runChecks(context.Background(), env)
	if r := resultNamed(t, results, "git on PATH"); r.Severity != SeverityUnhealthy {
		t.Errorf("git check = %s (%s), want UNHEALTHY", r.Severity, r.Detail)
	}
	if got := worstSeverity(results); got != SeverityUnhealthy {
		t.Errorf("worstSeverity = %s, want UNHEALTHY", got)
	}
}

func TestDoctorMissingPackageManagerIsUnhealthy(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.lookPath = func(file string) (string, error) {
		if file == "go" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + file, nil
	}

	r := resultNamed(t, runChecks(context.Background(), env), "package managers on PATH")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "go (needed by 1 project(s))") {
		t.Errorf("package manager check = %s (%s), want UNHEALTHY naming go", r.Severity, r.Detail)
	}
}

func TestDoctorUnsupportedSchemaVersionIsUnhealthy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	raw, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	err = raw.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists([]byte("meta"))
		if err != nil {
			return err
		}
		return meta.Put([]byte("schema_version"), []byte("99"))
	})
	if err != nil {
		t.Fatalf("seed schema version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	env := healthyDoctorEnv(t)
	env.store, env.storeErr = bboltstore.Open(path)
	if env.store != nil {
		t.Fatal("Open accepted a future schema version")
	}

	results := runChecks(context.Background(), env)
	schema := resultNamed(t, results, "schema version")
	if schema.Severity != SeverityUnhealthy || !strings.Contains(schema.Detail, "on-disk version 99") {
		t.Errorf("schema check = %s (%s), want UNHEALTHY naming version 99", schema.Severity, schema.Detail)
	}
	if r := resultNamed(t, results, "stale jobs"); r.Detail != "not checked: control DB unavailable" {
		t.Errorf("dependent check detail = %q, want it marked not checked", r.Detail)
	}
}

func TestDoctorStaleRunningJobWarns(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)
	jobs := []domain.Job{
		{ID: "job_stale", Type: "gc", State: domain.JobRunning, UpdatedAt: env.now.Add(-2 * time.Hour),
			Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Name: "pkg-old"}, Version: "0.9.0"}},
		{ID: "job_fresh", Type: "gc", State: domain.JobRunning, UpdatedAt: env.now.Add(-time.Minute)},
	}
	for _, j := range jobs {
		if err := env.store.PutJob(ctx, j); err != nil {
			t.Fatalf("PutJob: %v", err)
		}
	}

	results := runChecks(ctx, env)
	r := resultNamed(t, results, "stale jobs")
	if r.Severity != SeverityWarning || !strings.Contains(r.Detail, "1 job(s)") || !strings.Contains(r.Detail, "pkg-old 0.9.0") {
		t.Errorf("stale jobs = %s (%s), want WARN naming only the stale job", r.Severity, r.Detail)
	}
	if got := worstSeverity(results); got != SeverityWarning {
		t.Errorf("worstSeverity = %s, want WARN", got)
	}
}

func TestDoctorFlagsIncompleteActiveGeneration(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)
	bare := domain.Generation{ID: "gen_bare", Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/bare"}, Version: "v1.0.0"}}
	if err := env.store.PromoteGeneration(ctx, bare, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}

	results := runChecks(ctx, env)
	for _, name := range []string{"active generation manifests", "backend replicas"} {
		r := resultNamed(t, results, name)
		if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "example.com/bare") {
			t.Errorf("%s = %s (%s), want UNHEALTHY naming example.com/bare", name, r.Severity, r.Detail)
		}
	}
}

func TestDoctorFlagsDanglingActivePointer(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)
	gone := domain.Generation{ID: "gen_gone", Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/gone"}, Version: "v1.0.0"}}
	if err := env.store.PromoteGeneration(ctx, gone, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}
	if err := env.store.DeleteGenerationRecord(ctx, gone.ID); err != nil {
		t.Fatalf("DeleteGenerationRecord: %v", err)
	}

	r := resultNamed(t, runChecks(ctx, env), "active generations")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "missing generation gen_gone") {
		t.Errorf("active generations = %s (%s), want UNHEALTHY naming gen_gone", r.Severity, r.Detail)
	}
}

// TestDoctorFlagsEmptyActiveGeneration is POINT-003's own regression
// test: an ACTIVE generation whose manifest/replica/bbolt state all look
// fine (the fixture's other checks all stay OK) but whose points were
// silently overwritten by a sibling — the exact pre-POINT-001 failure
// class — must be caught even though nothing else in the pipeline
// noticed anything wrong.
func TestDoctorFlagsEmptyActiveGeneration(t *testing.T) {
	env := healthyDoctorEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz":
			return
		case strings.HasSuffix(r.URL.Path, "/points/count"):
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": 0}, "status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env.cfg.Vector.QdrantDefaults()
	env.cfg.Vector.Endpoint = srv.URL

	r := resultNamed(t, runChecks(context.Background(), env), "empty active generations")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "pkg-a") || !strings.Contains(r.Detail, "0 points") {
		t.Errorf("empty active generations = %s (%s), want UNHEALTHY naming pkg-a and 0 points", r.Severity, r.Detail)
	}
}

// TestDoctorEmptyActiveGenerationsSkipsGenerationsWithNoChunks covers the
// deliberate scope limit: a generation whose own manifest already
// claims zero chunks (nothing to embed at all — an edge case, not a
// bug) must never be flagged just because the backend also has zero
// points for it.
func TestDoctorEmptyActiveGenerationsSkipsGenerationsWithNoChunks(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)
	// A generation whose own manifest already claims zero chunks (the
	// fixture's pre-seeded pkg-a keeps its normal, nonzero 5) — the
	// backend below reports a nonzero count for every generation, so if
	// this generation gets flagged anyway, that would mean the check
	// wrongly queried the backend for it at all, not just wrongly
	// interpreted the result.
	seedActiveGeneration(t, env.store, env.badger, domain.EcosystemGo, "pkg-empty", "1.0.0", 0)

	r := resultNamed(t, runChecks(ctx, env), "empty active generations")
	if r.Severity != SeverityOK || strings.Contains(r.Detail, "pkg-empty") {
		t.Errorf("empty active generations = %s (%s), want OK and not naming pkg-empty (its manifest claims 0 chunks — nothing to have points for)", r.Severity, r.Detail)
	}
}

// TestDoctorEmptyActiveGenerationsChecksRealScaleConcurrently is OPS-003's
// (epic 61) own regression proof: many active generations must be
// checked with real overlap (a bounded worker pool), not one at a time —
// the exact shape that caused a real, live timeout at 558 generations
// against a degraded backend. A real overlap assertion, not just "the
// code compiles with a pool" (matching COORD-003's own precedent for
// proving concurrency).
func TestDoctorEmptyActiveGenerationsChecksRealScaleConcurrently(t *testing.T) {
	ctx := context.Background()
	store, badgerStore, _, _ := statusTestStores(t)

	const n = 40
	for i := range n {
		seedActiveGeneration(t, store, badgerStore, domain.EcosystemGo, fmt.Sprintf("pkg-%d", i), "1.0.0", 5)
	}

	var inFlight, maxInFlight atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/points/count") {
			return
		}
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			max := maxInFlight.Load()
			if cur <= max || maxInFlight.CompareAndSwap(max, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // long enough for overlap to be observable
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": 5}, "status": "ok"})
	}))
	t.Cleanup(srv.Close)

	env := &doctorEnv{cfg: config.Default(t.TempDir()), store: store, badger: badgerStore}
	env.cfg.Vector.QdrantDefaults()
	env.cfg.Vector.Endpoint = srv.URL

	r, detail := checkEmptyActiveGenerations(ctx, env)
	if r != SeverityOK || !strings.Contains(detail, fmt.Sprintf("%d checked", n)) {
		t.Fatalf("checkEmptyActiveGenerations = %s (%s), want OK naming %d checked", r, detail, n)
	}
	if got := maxInFlight.Load(); got <= 1 {
		t.Errorf("max concurrent Count calls observed = %d, want > 1 (real overlap, not serial)", got)
	}
	if got := maxInFlight.Load(); got > emptyGenerationsConcurrency {
		t.Errorf("max concurrent Count calls observed = %d, want <= the %d-worker bound", got, emptyGenerationsConcurrency)
	}
}

// TestDoctorEmptyActiveGenerationsReportsBudgetExceeded is OPS-003's
// other half: when the check can't finish within its own budget (a
// degraded backend, exactly the condition doctor exists to help
// diagnose), it must report a distinct, honest "skipped" result —
// never a bare timeout that silently aborts the whole doctor run, and
// never a false OK/UNHEALTHY verdict on generations it never actually
// checked.
func TestDoctorEmptyActiveGenerationsReportsBudgetExceeded(t *testing.T) {
	orig := emptyGenerationsCheckBudget
	emptyGenerationsCheckBudget = 50 * time.Millisecond
	t.Cleanup(func() { emptyGenerationsCheckBudget = orig })

	ctx := context.Background()
	store, badgerStore, _, _ := statusTestStores(t)
	seedActiveGeneration(t, store, badgerStore, domain.EcosystemGo, "pkg-slow", "1.0.0", 5)

	// A bounded sleep, not a wait on r.Context().Done(): the client's
	// budget-driven cancellation is what checkEmptyActiveGenerations
	// must react to, but the handler's own blocking time must stay
	// bounded regardless of whether/when the server notices that
	// cancellation, or httptest.Server.Close() (t.Cleanup below) could
	// hang the test indefinitely waiting for this handler to return.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/points/count") {
			return
		}
		time.Sleep(500 * time.Millisecond) // far longer than the 50ms shrunk budget
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"count": 5}, "status": "ok"})
	}))
	t.Cleanup(srv.Close)

	env := &doctorEnv{cfg: config.Default(t.TempDir()), store: store, badger: badgerStore}
	env.cfg.Vector.QdrantDefaults()
	env.cfg.Vector.Endpoint = srv.URL

	r, detail := checkEmptyActiveGenerations(ctx, env)
	if r != SeverityWarning || !strings.Contains(detail, "budget") {
		t.Errorf("checkEmptyActiveGenerations under a degraded backend = %s (%s), want WARN mentioning the check budget", r, detail)
	}
}

// TestDoctorFlagsReferencedButNeverBuilt is OPS-005's (epic 61) own
// regression proof, reproducing exactly the real bbolt state STRESS-006
// (epic 49) found live: a kill landing after a dependency's reference
// commits but before its matching build ever reaches ACTIVE. No prior
// doctor check catches this (there's no active generation at all for
// checkEmptyActiveGenerations to inspect), and `depctl describe` doesn't
// either — confirmed live, only checkReferencedButNeverBuilt does.
func TestDoctorFlagsReferencedButNeverBuilt(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)

	stuck := domain.VersionReference{
		ProjectID: "proj_go", Ecosystem: domain.EcosystemGo, Package: "example.com/stuck", Version: "v1.0.0",
		Reason: domain.ReferenceReasonProject, FirstSeenAt: env.now.Add(-time.Hour),
	}
	if err := env.store.AddReference(ctx, stuck); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	// PLAN-005: a plain sync now retries these, so one that's still
	// listed is a build that keeps failing; the advice points at the
	// daemon log rather than at --rebuild.
	r := resultNamed(t, runChecks(ctx, env), "referenced but never built")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "example.com/stuck") || !strings.Contains(r.Detail, "daemon log") {
		t.Errorf("referenced but never built = %s (%s), want UNHEALTHY naming example.com/stuck and pointing at the daemon log", r.Severity, r.Detail)
	}
}

// TestDoctorReportsNoSourceVersionsAsWarningNotStuck: a version sync has
// recorded as having no docs source is intentionally unbuilt (PLAN-005),
// so it's a WARN, never UNHEALTHY "stuck".
func TestDoctorReportsNoSourceVersionsAsWarningNotStuck(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)

	ref := domain.VersionReference{
		ProjectID: "proj_go", Ecosystem: domain.EcosystemGo, Package: "example.com/nosource", Version: "v1.0.0",
		Reason: domain.ReferenceReasonProject, FirstSeenAt: env.now.Add(-time.Hour),
	}
	if err := env.store.AddReference(ctx, ref); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	if err := env.store.PutNoSourceVersion(ctx, bboltstore.NoSourceVersion{Ecosystem: domain.EcosystemGo, Package: "example.com/nosource", Version: "v1.0.0"}); err != nil {
		t.Fatalf("PutNoSourceVersion: %v", err)
	}

	r := resultNamed(t, runChecks(ctx, env), "referenced but never built")
	if r.Severity != SeverityWarning || !strings.Contains(r.Detail, "example.com/nosource") || !strings.Contains(r.Detail, "no known docs source") {
		t.Errorf("referenced but never built = %s (%s), want WARN naming example.com/nosource as having no docs source", r.Severity, r.Detail)
	}
}

// TestDoctorReferencedButNeverBuiltSkipsRecentReferences confirms the
// check never false-positives on a dependency that's still genuinely,
// legitimately mid-first-sync — only a reference older than the grace
// window is ever reported.
func TestDoctorReferencedButNeverBuiltSkipsRecentReferences(t *testing.T) {
	ctx := context.Background()
	env := healthyDoctorEnv(t)

	fresh := domain.VersionReference{
		ProjectID: "proj_go", Ecosystem: domain.EcosystemGo, Package: "example.com/mid-sync", Version: "v1.0.0",
		Reason: domain.ReferenceReasonProject, FirstSeenAt: env.now.Add(-1 * time.Second),
	}
	if err := env.store.AddReference(ctx, fresh); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	r := resultNamed(t, runChecks(ctx, env), "referenced but never built")
	if r.Severity != SeverityOK || strings.Contains(r.Detail, "example.com/mid-sync") {
		t.Errorf("referenced but never built = %s (%s), want OK and not naming a reference only 1s old (still plausibly mid-first-sync)", r.Severity, r.Detail)
	}
}

func TestDoctorEmbeddingModelMismatchIsUnhealthy(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Embedding.Model = "some-newer-model"

	r := resultNamed(t, runChecks(context.Background(), env), "embedding model compatibility")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, testEmbeddingModel) {
		t.Errorf("embedding check = %s (%s), want UNHEALTHY naming %s", r.Severity, r.Detail, testEmbeddingModel)
	}
}

func TestDoctorNoActiveGenerationsWarns(t *testing.T) {
	store, badgerStore, _, _ := statusTestStores(t)
	env := healthyDoctorEnv(t)
	env.store, env.badger = store, badgerStore

	r := resultNamed(t, runChecks(context.Background(), env), "active generations")
	if r.Severity != SeverityWarning {
		t.Errorf("active generations on an empty store = %s (%s), want WARN", r.Severity, r.Detail)
	}
}

// TestDoctorEmbeddingBackendNoDaemonProbesDirectly covers runDoctorDirect's
// path: with no daemon (env.embeddingReadiness nil), checkEmbeddingBackend
// falls back to a synchronous probe against the configured Ollama endpoint,
// bounded by the same timeout checkEmbeddingReadiness itself uses.
func TestDoctorEmbeddingBackendNoDaemonProbesDirectly(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Embedding.Endpoint = deadBackendURL(t)

	r := resultNamed(t, runChecks(context.Background(), env), "embedding backend")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "unreachable") {
		t.Errorf("embedding backend check = %s (%s), want UNHEALTHY mentioning unreachable", r.Severity, r.Detail)
	}
}

// In auto mode an unreachable Ollama is a supported state: doctor says
// keyword search is in use, without calling it unhealthy.
func TestDoctorEmbeddingBackendUnreachableIsOKInAutoMode(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Retrieval.Mode = config.RetrievalAuto
	env.embeddingReadiness = newEmbeddingReadiness()
	env.embeddingReadiness.set(embeddingStateUnreachable, "Ollama not reachable at http://127.0.0.1:1")

	r := resultNamed(t, runChecks(context.Background(), env), "embedding backend")
	if r.Severity != SeverityOK || !strings.Contains(r.Detail, "keyword search") {
		t.Errorf("embedding backend check = %s (%s), want OK saying keyword search is in use", r.Severity, r.Detail)
	}
}

// Keyword mode never uses an embedder or vector store: doctor says so
// rather than probing them (or calling them down).
func TestDoctorKeywordModeReportsEmbedderAndVectorStoreAsNotUsed(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Retrieval.Mode = config.RetrievalKeyword
	env.cfg.Embedding.Endpoint = deadBackendURL(t)

	results := runChecks(context.Background(), env)
	for _, name := range []string{"embedding backend", "vector backend", "embedding model compatibility"} {
		r := resultNamed(t, results, name)
		if r.Severity != SeverityOK || !strings.Contains(r.Detail, "not used") {
			t.Errorf("%s = %s (%s), want OK and \"not used\"", name, r.Severity, r.Detail)
		}
	}
	if r := resultNamed(t, results, "config"); !strings.Contains(r.Detail, "retrieval keyword") {
		t.Errorf("config = %q, want it to name keyword retrieval", r.Detail)
	}
}

func TestDoctorVectorBackendNoDaemonProbesDirectly(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Vector.QdrantDefaults()
	env.cfg.Vector.Endpoint = deadBackendURL(t)
	env.cfg.Vector.Managed = false // exercise the plain report-only path, not WATCH-016's bootstrap

	r := resultNamed(t, runChecks(context.Background(), env), "vector backend")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "unreachable") {
		t.Errorf("vector backend check = %s (%s), want UNHEALTHY mentioning unreachable", r.Severity, r.Detail)
	}
}

// TestDoctorEmbeddingBackendReflectsLiveDaemonState covers the daemon
// path: when env.embeddingReadiness is set (as engine.Doctor sets it),
// checkEmbeddingBackend must read that live state rather than probing
// itself — the whole point of WATCH-014's background check.
func TestDoctorEmbeddingBackendReflectsLiveDaemonState(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.embeddingReadiness = newEmbeddingReadiness()
	env.embeddingReadiness.set(embeddingStateUnreachable, "Ollama not reachable at http://127.0.0.1:1")

	r := resultNamed(t, runChecks(context.Background(), env), "embedding backend")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "unreachable") {
		t.Errorf("embedding backend check = %s (%s), want UNHEALTHY reflecting the daemon's own state", r.Severity, r.Detail)
	}
}

func TestDoctorVectorBackendReflectsLiveDaemonState(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.vectorReadiness = newVectorReadiness()
	env.vectorReadiness.set(vectorStateStarting, "depctl-qdrant")

	r := resultNamed(t, runChecks(context.Background(), env), "vector backend")
	if r.Severity != SeverityWarning || !strings.Contains(r.Detail, "depctl-qdrant") {
		t.Errorf("vector backend check = %s (%s), want WARN naming the starting container", r.Severity, r.Detail)
	}
}

// TestDoctorVectorBackendReadyDetailDistinguishesManaged is WATCH-017's
// last acceptance criterion: once a backend is ready, the detail line
// must say whether it's depctl's own managed container or a
// user-supplied one, not just "ready".
func TestDoctorVectorBackendReadyDetailDistinguishesManaged(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Vector.Managed = true

	r := resultNamed(t, runChecks(context.Background(), env), "vector backend")
	if r.Severity != SeverityOK || !strings.Contains(r.Detail, "reachable at") || !strings.Contains(r.Detail, "managed: "+qdrantContainerName) {
		t.Errorf("vector backend check = %s (%s), want OK naming the endpoint and the managed container", r.Severity, r.Detail)
	}

	env.cfg.Vector.Managed = false
	r = resultNamed(t, runChecks(context.Background(), env), "vector backend")
	if r.Severity != SeverityOK || !strings.Contains(r.Detail, "reachable at") || strings.Contains(r.Detail, "managed") {
		t.Errorf("vector backend check = %s (%s), want OK naming just the endpoint, no managed mention", r.Severity, r.Detail)
	}
}

func TestSeverityValuesAreExitCodes(t *testing.T) {
	if SeverityOK != 0 || SeverityWarning != 1 || SeverityUnhealthy != 2 {
		t.Fatalf("severity values = %d/%d/%d, want 0/1/2 (they are doctor's exit codes)", SeverityOK, SeverityWarning, SeverityUnhealthy)
	}
	results := []CheckResult{{Severity: SeverityOK}, {Severity: SeverityWarning}, {Severity: SeverityOK}}
	if got := worstSeverity(results); got != SeverityWarning {
		t.Errorf("worstSeverity = %s, want WARN", got)
	}
}

func TestDoctorCommandReturnsExitCode(t *testing.T) {
	isolateEnv(t)
	endpoint := deadBackendURL(t)
	// managed: false, so doctor only reports the outage instead of also
	// trying to start a Qdrant container (which on CI runs Podman under
	// the test's temp HOME and leaves undeletable storage behind).
	writeTestConfig(t, func(c *config.Config) {
		c.Vector.QdrantDefaults()
		c.Vector.Endpoint, c.Vector.Managed = endpoint, false
	})

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"doctor"})
	err := root.Execute()

	var exitErr ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("doctor with backend down = %v, want ExitCodeError{Code: 2}\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "vector backend reachable") || !strings.Contains(out.String(), "unhealthy") {
		t.Errorf("report missing expected lines:\n%s", out.String())
	}
}

// Vectors made with different prompts or a different size don't mix
// with what search will now ask for, even under the same model name.
func TestDoctorFlagsEmbeddingPromptOrSizeChange(t *testing.T) {
	env := healthyDoctorEnv(t)
	env.cfg.Embedding.Dimensions = 256 // the replica's vectors were made at full size
	r := resultNamed(t, runChecks(context.Background(), env), "embedding model compatibility")
	if r.Severity != SeverityUnhealthy || !strings.Contains(r.Detail, "256d") {
		t.Errorf("embedding model compatibility = %s (%s), want UNHEALTHY naming the new 256d identity", r.Severity, r.Detail)
	}
}

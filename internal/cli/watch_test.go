package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/config"
	"github.com/aleutian-ai/depctl/internal/daemon"
)

// syncBuffer is a bytes.Buffer safe to read while another goroutine
// writes command output into it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deadEndpointsConfig points embedding and vector at nothing, so any
// sync work fails fast instead of reaching a real local service.
// Vector.Managed is forced off: WATCH-016's managed-bootstrap attempt
// would otherwise mask the plain "unreachable" outcome these tests
// expect whenever a container runtime happens to be on the test
// machine's PATH.
func deadEndpointsConfig(t *testing.T, mutate func(*config.Config)) {
	t.Helper()
	dead := deadBackendURL(t)
	writeTestConfig(t, func(c *config.Config) {
		c.Embedding.Endpoint = dead
		c.Vector.QdrantDefaults()
		c.Vector.Endpoint = dead
		c.Vector.Managed = false
		if mutate != nil {
			mutate(c)
		}
	})
}

func projectIDForRoot(t *testing.T, root string) string {
	t.Helper()
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range projects {
		if p.Root == root {
			return p.ID
		}
	}
	t.Fatalf("no project registered at %s", root)
	return ""
}

// addBarDependency makes the scanDepFixture app also depend on a second
// local module, example.com/bar.
func addBarDependency(t *testing.T, appRoot string) {
	t.Helper()
	writeGoMod(t, filepath.Join(filepath.Dir(appRoot), "barlocal"), "module example.com/bar\n\ngo 1.21\n")
	writeGoMod(t, appRoot, "module example.com/app\n\ngo 1.21\n\nrequire (\n\texample.com/foo v0.0.0\n\texample.com/bar v0.0.0\n)\n\nreplace example.com/foo => ../foolocal\n\nreplace example.com/bar => ../barlocal\n")
}

// testEngine builds the daemon's engine over stores this test owns,
// exercising exactly what the daemon runs without a daemon process.
func testEngine(t *testing.T) *engine {
	t.Helper()
	cfg, err := loadDepctlConfig()
	if err != nil {
		t.Fatalf("loadDepctlConfig: %v", err)
	}
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	badgerStore, err := openDataStore()
	if err != nil {
		t.Fatalf("openDataStore: %v", err)
	}
	t.Cleanup(func() { badgerStore.Close() })

	controlPath, err := controlDBPath()
	if err != nil {
		t.Fatalf("controlDBPath: %v", err)
	}
	badgerPath, err := badgerDirPath()
	if err != nil {
		t.Fatalf("badgerDirPath: %v", err)
	}
	return &engine{store: store, badgerStore: badgerStore, cfg: cfg, controlPath: controlPath, badgerPath: badgerPath}
}

func TestEngineSyncResolvesFirstWhenAsked(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealDepctlBinary(t)
	deadEndpointsConfig(t, nil)
	root := scanDepFixture(t)
	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before opening the store directly below
	// (projectIDForRoot, and testEngine's store).
	stopRunningDaemon(t)
	id := projectIDForRoot(t, root)
	addBarDependency(t, root)

	e := testEngine(t)
	var out bytes.Buffer
	if _, err := e.Sync(context.Background(), daemon.NewBuildCoordinator(), id, daemon.SyncOptions{Resolve: true}, &out); err != nil {
		t.Fatalf("Sync: %v\n%s", err, out.String())
	}

	res, err := e.store.GetResolution(context.Background(), id)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	var names []string
	for _, d := range res.Dependencies {
		names = append(names, d.Dependency.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "example.com/bar") {
		t.Errorf("resolution after sync = %v, want example.com/bar", names)
	}

	refs, err := e.store.ListAllReferences(context.Background())
	if err != nil {
		t.Fatalf("ListAllReferences: %v", err)
	}
	var barRef bool
	for _, r := range refs {
		barRef = barRef || (r.ProjectID == id && r.Package == "example.com/bar")
	}
	if !barRef {
		t.Errorf("references after sync = %+v, want one for example.com/bar (plan ran and recorded it)", refs)
	}
}

func TestEngineProjectsListsResolvedProjects(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealDepctlBinary(t)
	deadEndpointsConfig(t, nil)
	root := scanDepFixture(t)
	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before testEngine opens the store
	// directly.
	stopRunningDaemon(t)

	e := testEngine(t)
	projects, err := e.Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 || projects[0].Root != root {
		t.Fatalf("Projects = %+v, want just the scanned fixture at %s", projects, root)
	}
	if projects[0].Ecosystem == "" {
		t.Error("project has no ecosystem; the watcher needs it to know which manifests to watch")
	}
}

func TestWatchCommandPointsAtTheDaemon(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	startDaemon(t)

	out := runCommandOutput(t, "watch")
	if !strings.Contains(out, "managed by the depctl daemon") {
		t.Errorf("watch output = %q, want it to say watching is daemon-managed", out)
	}
	if !strings.Contains(out, "watching registered projects") {
		t.Errorf("watch output = %q, want it to report that watching is on", out)
	}
}

func TestWatchCommandReportsWatchingDisabled(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	writeTestConfig(t, func(c *config.Config) { c.Watch.Enabled = false })
	startDaemon(t)

	out := runCommandOutput(t, "watch")
	if !strings.Contains(out, "watching is off") {
		t.Errorf("watch output = %q, want it to report watch.enabled: false", out)
	}
}

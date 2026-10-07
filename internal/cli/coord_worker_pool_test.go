package cli

import (
	"aleutian-ai/ragctl/internal/daemon/api"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/daemon"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/embedding"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/registry"
)

// trackingEmbedder records how many concurrent Embed calls are ever in
// flight at once (maxSeen) — real overlap proof for COORD-003's worker
// pool, the same inFlight/maxSeen high-water-mark pattern
// internal/daemon/scheduler_test.go's fakeSync already uses. A small
// fixed delay makes overlap actually observable instead of racing to
// finish before a second worker even starts.
type trackingEmbedder struct {
	atomicPromotionFakeEmbedder
	delay    time.Duration
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (e *trackingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	now := e.inFlight.Add(1)
	defer e.inFlight.Add(-1)
	for {
		seen := e.maxSeen.Load()
		if now <= seen || e.maxSeen.CompareAndSwap(seen, now) {
			break
		}
	}
	time.Sleep(e.delay)
	return e.atomicPromotionFakeEmbedder.Embed(ctx, texts)
}

// twoDependencyQueueFixture builds a syncQueue with two SYNC_VERSION
// actions for two wholly distinct, real fixture dependencies
// (example.com/widget, example.com/gadget) — enough to prove genuine
// two-worker overlap without needing a larger fixture set.
func twoDependencyQueueFixture(t *testing.T) (*crossProjectTestFixture, *syncQueue, *registry.Registry) {
	t.Helper()
	requireGitForAtomicPromotionTest(t)
	f := newCrossProjectTestFixture(t)

	repoWidget := atomicPromotionFixtureRepo(t)
	repoGadget := gadgetFixtureRepo(t)

	// One registry covering both — matches how a real RunSync call
	// resolves every dependency's source through the same loaded
	// registry, not one per dependency.
	reg := combinedTestRegistry(t, repoWidget, repoGadget)

	depWidget := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}
	depGadget := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/gadget"},
		Version:    "v1.0.0",
	}

	actions := []planner.Action{
		{Kind: planner.ActionSyncVersion, ProjectID: "proj_widget", Dependency: depWidget},
		{Kind: planner.ActionSyncVersion, ProjectID: "proj_gadget", Dependency: depGadget},
	}
	return f, newSyncQueue(actions), reg
}

// combinedTestRegistry loads one registry with manifests for both
// example.com/widget (repoWidget) and example.com/gadget (repoGadget) —
// two separate manifest files in the same registry directory.
func combinedTestRegistry(t *testing.T, repoWidget, repoGadget string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "widget.yaml", sprintfManifest(atomicPromotionManifestYAML, repoWidget))
	writeFile(t, dir, "gadget.yaml", sprintfManifest(gadgetManifestYAML, repoGadget))
	reg, err := registry.NewLoader(dir, "").Load(context.Background())
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

func sprintfManifest(tmpl, repoDir string) string {
	return fmt.Sprintf(tmpl, repoDir)
}

// noopWriteLine discards runSyncAction's progress output for tests that
// don't care about it, matching how RunSync's own writeLine works, just
// pointed nowhere.
func noopWriteLine(string, ...any) {}

// TestWorkerPoolProcessesActionsConcurrently is COORD-003's own overlap
// proof: two workers pulling from one syncQueue, two distinct
// dependencies with a real, measurable delay in their embed step — both
// must be genuinely in flight at once, not one finishing before the
// other starts.
func TestWorkerPoolProcessesActionsConcurrently(t *testing.T) {
	f, queue, reg := twoDependencyQueueFixture(t)
	coordinator := daemon.NewBuildCoordinator()

	shared := &trackingEmbedder{atomicPromotionFakeEmbedder: atomicPromotionFakeEmbedder{dims: 4}, delay: 100 * time.Millisecond}
	getPipeline := func() (*syncPipeline, error) {
		return &syncPipeline{embedder: &embedding.Prompted{Embedder: shared}, vb: f.vb, gitCache: f.gitCache, ns: f.ns}, nil
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				action, ok := queue.next(nil)
				if !ok {
					return
				}
				s, fl, _ := runSyncAction(context.Background(), coordinator, f.store, f.badgerStore, reg, getPipeline, action, false, false, nil, noopWriteLine)
				if fl != 0 {
					t.Errorf("action for %s failed unexpectedly (synced=%d failed=%d)", action.Dependency.Dependency.Name, s, fl)
				}
			}
		}()
	}
	wg.Wait()

	if got := shared.maxSeen.Load(); got < 2 {
		t.Errorf("max concurrent embed calls observed = %d, want 2 (the two workers never genuinely overlapped)", got)
	}
}

// TestWorkerPoolIsolatesOneFailureFromOthers is COORD-003's required
// failure-isolation test: one dependency fails, one is slow, the rest
// must complete normally regardless — explicitly not errgroup's default
// cancel-on-first-error behavior (see the ticket's own design note).
func TestWorkerPoolIsolatesOneFailureFromOthers(t *testing.T) {
	f, queue, reg := twoDependencyQueueFixture(t)
	coordinator := daemon.NewBuildCoordinator()

	// widget's embedder fails outright; gadget's succeeds normally —
	// proves gadget's own outcome is untouched by widget's failure.
	failing := &atomicPromotionFakeEmbedder{dims: 4, failAfter: 1}
	ok := &atomicPromotionFakeEmbedder{dims: 4}
	getPipeline := func() (*syncPipeline, error) {
		// Route by which dependency is being built: runSyncAction calls
		// getPipeline per-action, so a small router keyed on the
		// in-flight action's own dependency name is enough — each
		// worker only ever asks for the pipeline while processing one
		// specific action at a time.
		return &syncPipeline{embedder: &embedding.Prompted{Embedder: &routingEmbedder{failing: failing, ok: ok}}, vb: f.vb, gitCache: f.gitCache, ns: f.ns}, nil
	}

	results := map[string]int{} // dependency name -> failed count
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				action, ok := queue.next(nil)
				if !ok {
					return
				}
				_, fl, _ := runSyncAction(context.Background(), coordinator, f.store, f.badgerStore, reg, getPipeline, action, false, false, nil, noopWriteLine)
				mu.Lock()
				results[action.Dependency.Dependency.Name] = fl
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if results["example.com/widget"] != 1 {
		t.Errorf("example.com/widget failed = %d, want 1 (its embedder was set to fail)", results["example.com/widget"])
	}
	if results["example.com/gadget"] != 0 {
		t.Errorf("example.com/gadget failed = %d, want 0 (a different dependency's failure must never affect it)", results["example.com/gadget"])
	}

	active, err := f.store.GetActiveGeneration(context.Background(), domain.EcosystemGo, "example.com/gadget", "v1.0.0", f.vb.Name())
	if err != nil || active.State != domain.GenActive {
		t.Errorf("example.com/gadget's generation = %+v, err=%v, want ACTIVE despite widget's concurrent failure", active, err)
	}
}

// routingEmbedder dispatches to a per-dependency fake based on which
// text is being embedded — real chunk content always includes real
// source text, so a name match is a reliable enough router for a test
// fixture with two deliberately distinct, named fixture packages.
type routingEmbedder struct {
	failing *atomicPromotionFakeEmbedder
	ok      *atomicPromotionFakeEmbedder
}

func (r *routingEmbedder) Name() string                                { return "fake" }
func (r *routingEmbedder) ModelID() string                             { return "fake-model" }
func (r *routingEmbedder) Dimensions(ctx context.Context) (int, error) { return 4, nil }
func (r *routingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, t := range texts {
		if strings.Contains(t, "Widget") {
			return r.failing.Embed(ctx, texts)
		}
	}
	return r.ok.Embed(ctx, texts)
}

// TestWorkerPoolHonorsPriorityBump proves WATCH-020's bump-to-front
// semantics survive the move to concurrent workers: a bumped dependency
// is picked up promptly by whichever worker asks next, not stuck behind
// the queue's default order.
func TestWorkerPoolHonorsPriorityBump(t *testing.T) {
	dep := func(name string) planner.Action {
		return planner.Action{
			Kind:       planner.ActionSyncVersion,
			ProjectID:  "proj_" + name,
			Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/" + name}, Version: "v1.0.0"},
		}
	}
	queue := newSyncQueue([]planner.Action{dep("a"), dep("b"), dep("c"), dep("z")})

	priority := &daemon.SyncPriority{}
	priority.Bump("example.com/z")

	action, ok := queue.next(priority)
	if !ok {
		t.Fatal("queue.next reported empty on the first pop")
	}
	if action.Dependency.Dependency.Name != "example.com/z" {
		t.Errorf("first popped action = %s, want the bumped example.com/z", action.Dependency.Dependency.Name)
	}
}

// hangingEmbedder never returns on its own — it only unblocks when ctx
// is cancelled, the same way a real network call bound to a context
// behaves once that context's deadline fires.
type hangingEmbedder struct{}

func (hangingEmbedder) Name() string                                { return "fake" }
func (hangingEmbedder) ModelID() string                             { return "fake-model" }
func (hangingEmbedder) Dimensions(ctx context.Context) (int, error) { return 4, nil }
func (hangingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunSyncDependencyTimeoutDoesNotWedgeOtherWorkers is COORD-003's
// replacement for the scheduler-level timeout test COORD-002 removed
// (internal/daemon/scheduler_test.go's execute() no longer bounds sync
// work at all — see that file's TestGCActionTimesOutWithoutWedgingQueue
// comment): dependencySyncTimeout, not an outer scheduler ceiling, is
// what actually stops one hung dependency from starving the rest.
// widget hangs forever (ctx-respecting, like a real network call);
// gadget must still complete normally on the other worker.
func TestRunSyncDependencyTimeoutDoesNotWedgeOtherWorkers(t *testing.T) {
	// Short enough to keep the test fast, generous enough that gadget's
	// own real work (real git clone, real bbolt/Badger writes, real
	// chunking, all genuinely slower than 50ms under -race's overhead —
	// confirmed by this test itself, which failed at 50ms before this
	// value was picked) never gets caught by the same shortened ceiling
	// meant only for widget's artificial hang.
	original := dependencySyncTimeout
	dependencySyncTimeout = 3 * time.Second
	t.Cleanup(func() { dependencySyncTimeout = original })

	f, queue, reg := twoDependencyQueueFixture(t)
	coordinator := daemon.NewBuildCoordinator()

	getPipeline := func() (*syncPipeline, error) {
		return &syncPipeline{embedder: &embedding.Prompted{Embedder: hangingEmbedder{}}, vb: f.vb, gitCache: f.gitCache, ns: f.ns}, nil
	}
	// gadget's own worker uses a real, fast embedder instead — routed
	// the same way TestWorkerPoolIsolatesOneFailureFromOthers routes by
	// dependency, so gadget never touches the hanging one at all.
	fastGetPipeline := func() (*syncPipeline, error) {
		return &syncPipeline{embedder: &embedding.Prompted{Embedder: &atomicPromotionFakeEmbedder{dims: 4}}, vb: f.vb, gitCache: f.gitCache, ns: f.ns}, nil
	}

	results := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				action, ok := queue.next(nil)
				if !ok {
					return
				}
				gp := fastGetPipeline
				if action.Dependency.Dependency.Name == "example.com/widget" {
					gp = getPipeline
				}
				_, fl, _ := runSyncAction(context.Background(), coordinator, f.store, f.badgerStore, reg, gp, action, false, false, nil, noopWriteLine)
				mu.Lock()
				results[action.Dependency.Dependency.Name] = fl
				mu.Unlock()
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker pool never finished — widget's hang wedged the whole pool instead of just its own worker")
	}

	if results["example.com/widget"] != 1 {
		t.Errorf("example.com/widget failed = %d, want 1 (dependencySyncTimeout should have cut it off)", results["example.com/widget"])
	}
	if results["example.com/gadget"] != 0 {
		t.Errorf("example.com/gadget failed = %d, want 0 (widget's hang must never affect it)", results["example.com/gadget"])
	}
}

// snapshottingEmbedder records the tracker's view from inside each Embed
// call — i.e. mid-replicate, while a dependency is genuinely in flight.
type snapshottingEmbedder struct {
	atomicPromotionFakeEmbedder
	progress *daemon.SyncProgress
	mu       sync.Mutex
	seen     []api.InFlightDependency
}

func (e *snapshottingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	snap := e.progress.Snapshot()
	e.mu.Lock()
	e.seen = append(e.seen, snap.InFlight...)
	e.mu.Unlock()
	return e.atomicPromotionFakeEmbedder.Embed(ctx, texts)
}

// TestWorkerPoolReportsProgressAcrossConcurrentWorkers is SCOPE-001's
// check against the real pool and real Replicate: every action is
// counted done exactly once, nothing is left in flight, and while a
// dependency was being embedded the tracker showed it with a known chunk
// total — the coarse "43 of 560" counter alone would have looked frozen.
func TestWorkerPoolReportsProgressAcrossConcurrentWorkers(t *testing.T) {
	f, queue, reg := twoDependencyQueueFixture(t)
	coordinator := daemon.NewBuildCoordinator()
	progress := &daemon.SyncProgress{}
	progress.SetTotal(2)

	emb := &snapshottingEmbedder{atomicPromotionFakeEmbedder: atomicPromotionFakeEmbedder{dims: 4}, progress: progress}
	getPipeline := func() (*syncPipeline, error) {
		return &syncPipeline{embedder: &embedding.Prompted{Embedder: emb}, vb: f.vb, gitCache: f.gitCache, ns: f.ns}, nil
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				action, ok := queue.next(nil)
				if !ok {
					return
				}
				runSyncAction(context.Background(), coordinator, f.store, f.badgerStore, reg, getPipeline, action, false, false, progress, noopWriteLine)
			}
		}()
	}
	wg.Wait()

	snap := progress.Snapshot()
	if snap.Done != 2 || snap.Failed != 0 || snap.Total != 2 || len(snap.InFlight) != 0 {
		t.Errorf("final progress = %+v, want 2 of 2 done, none failed, nothing in flight", snap)
	}

	emb.mu.Lock()
	defer emb.mu.Unlock()
	var sawChunkTotal bool
	for _, d := range emb.seen {
		if d.ChunksTotal > 0 {
			sawChunkTotal = true
		}
	}
	if !sawChunkTotal {
		t.Errorf("no in-flight snapshot during embedding had a chunk total: %+v", emb.seen)
	}
}

// TestWorkerPoolProgressCountsFailures: a failed action is still done,
// and counted failed — the numbers reach total even when some fail.
func TestWorkerPoolProgressCountsFailures(t *testing.T) {
	f, queue, reg := twoDependencyQueueFixture(t)
	coordinator := daemon.NewBuildCoordinator()
	progress := &daemon.SyncProgress{}

	failing := &atomicPromotionFakeEmbedder{dims: 4, failAfter: 1}
	ok := &atomicPromotionFakeEmbedder{dims: 4}
	getPipeline := func() (*syncPipeline, error) {
		return &syncPipeline{embedder: &embedding.Prompted{Embedder: &routingEmbedder{failing: failing, ok: ok}}, vb: f.vb, gitCache: f.gitCache, ns: f.ns}, nil
	}
	for {
		action, more := queue.next(nil)
		if !more {
			break
		}
		runSyncAction(context.Background(), coordinator, f.store, f.badgerStore, reg, getPipeline, action, false, false, progress, noopWriteLine)
	}
	if snap := progress.Snapshot(); snap.Done != 2 || snap.Failed != 1 {
		t.Errorf("progress = %+v, want 2 done, 1 failed", snap)
	}
}

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/source/git"
)

// barrierEmbedder blocks its first Embed call until release is closed —
// stands in for "this project's build is genuinely deep inside a real
// acquisition/embed step," without needing to fake an HTTP Ollama
// server. See COORD-002's own primary regression test design
// (docs/tickets/planned/53-foreground-jit-isolation).
//
// syncVersion's real pipeline calls Embed more than once per build —
// Replicate embeds the generation's own chunks, and
// validate.VersionCorrectness separately re-embeds a sample afterward
// to check for cross-contamination — so only the *first* call blocks
// and signals started; later calls (once release is already closed)
// pass straight through, same as a real embedder would once the
// barrier's real-world equivalent (Ollama actually responding) has
// already happened.
type barrierEmbedder struct {
	dims    int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBarrierEmbedder(dims int) *barrierEmbedder {
	return &barrierEmbedder{dims: dims, started: make(chan struct{}), release: make(chan struct{})}
}

func (f *barrierEmbedder) Name() string    { return "fake" }
func (f *barrierEmbedder) ModelID() string { return "fake-model" }
func (f *barrierEmbedder) Dimensions(ctx context.Context) (int, error) {
	return f.dims, nil
}

func (f *barrierEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.once.Do(func() { close(f.started) })
	<-f.release
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dims)
		for j := range v {
			v[j] = float32(len(t) + j)
		}
		out[i] = v
	}
	return out, nil
}

// gadgetFixtureRepo creates a one-tag fixture repo for a second, wholly
// distinct dependency (example.com/gadget) — kept separate from
// atomicPromotionFixtureRepo's example.com/widget so two projects'
// builds in the same test never share a generation identity.
func gadgetFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	atomicPromotionRunGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Gadget\n\nA small, unrelated example package.\n")
	atomicPromotionRunGit(t, dir, "add", ".")
	atomicPromotionRunGit(t, dir, "commit", "-q", "-m", "v1.0.0")
	atomicPromotionRunGit(t, dir, "tag", "v1.0.0")
	return dir
}

const gadgetManifestYAML = `apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: example.com/gadget
match:
  ecosystems: [go]
  packages: [example.com/gadget]
version:
  strategy: none
sources:
  - id: repository
    type: git
    url: %[1]s
    ref: v${version}
    authority: 100
`

func gadgetTestRegistry(t *testing.T, repoDir string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	content := fmt.Sprintf(gadgetManifestYAML, repoDir)
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	reg, err := registry.NewLoader(dir, "").Load(context.Background())
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

// crossProjectTestFixture bundles what both COORD-002 tests below need:
// real bbolt/Badger stores, a real git cache, a fake (but real-shaped)
// vector backend, and a shared BuildCoordinator.
type crossProjectTestFixture struct {
	store       *bboltstore.Store
	badgerStore *badgerstore.Store
	gitCache    *git.Cache
	vb          *backendtest.Backend
	ns          backend.Namespace
	coordinator *daemon.BuildCoordinator
}

func newCrossProjectTestFixture(t *testing.T) *crossProjectTestFixture {
	t.Helper()
	store, err := bboltstore.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	badgerStore, err := badgerstore.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { badgerStore.Close() })

	return &crossProjectTestFixture{
		store:       store,
		badgerStore: badgerStore,
		gitCache:    git.NewCache(t.TempDir()),
		vb:          backendtest.New(),
		ns:          backend.Namespace{Name: "ragctl", Dimensions: 4},
		coordinator: daemon.NewBuildCoordinator(),
	}
}

// TestCrossProjectJITNotStrandedBehindBulkSync is COORD-002's primary
// regression test: project B's build is deliberately held open inside
// its embed step (barrierEmbedder), and project A's independent build —
// a different dependency entirely — must complete while B is still
// blocked. This is the real syncVersion/BuildCoordinator integration
// path, not just the scheduler's generic dispatch — see
// internal/daemon/scheduler_test.go's
// TestSchedulerRunsDifferentProjectsConcurrently for that half of the
// proof.
func TestCrossProjectJITNotStrandedBehindBulkSync(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()
	f := newCrossProjectTestFixture(t)

	repoB := atomicPromotionFixtureRepo(t)
	regB := atomicPromotionTestRegistry(t, repoB)
	depB := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}

	repoA := gadgetFixtureRepo(t)
	regA := gadgetTestRegistry(t, repoA)
	depA := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/gadget"},
		Version:    "v1.0.0",
	}

	blockedEmbedder := newBarrierEmbedder(4)
	fastEmbedder := &atomicPromotionFakeEmbedder{dims: 4}

	actionB := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_b", Dependency: depB}
	actionA := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_a", Dependency: depA}

	bDone := make(chan error, 1)
	go func() {
		bDone <- f.coordinator.Build(depB, func() error {
			return syncVersion(ctx, f.store, f.badgerStore, f.gitCache, blockedEmbedder, f.vb, f.ns, regB, actionB, false)
		})
	}()

	select {
	case <-blockedEmbedder.started:
	case <-time.After(5 * time.Second):
		t.Fatal("project B's build never reached its embed step")
	}

	// Project A's build must complete while B is still blocked inside
	// its own embed call — the actual claim this test exists to prove.
	aDone := make(chan error, 1)
	go func() {
		aDone <- f.coordinator.Build(depA, func() error {
			return syncVersion(ctx, f.store, f.badgerStore, f.gitCache, fastEmbedder, f.vb, f.ns, regA, actionA, false)
		})
	}()

	select {
	case err := <-aDone:
		if err != nil {
			t.Fatalf("project A's build: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("project A's build never completed — it was stranded behind project B's unrelated, still-blocked build")
	}

	activeA, err := f.store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/gadget", f.vb.Name())
	if err != nil || activeA.State != domain.GenActive {
		t.Fatalf("project A's generation after Build = %+v, err=%v, want an ACTIVE generation", activeA, err)
	}

	close(blockedEmbedder.release)
	if err := <-bDone; err != nil {
		t.Fatalf("project B's build: %v", err)
	}
	activeB, err := f.store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", f.vb.Name())
	if err != nil || activeB.State != domain.GenActive {
		t.Fatalf("project B's generation after Build = %+v, err=%v, want an ACTIVE generation", activeB, err)
	}
}

// TestSameDependencyAcrossProjectsCoalescesIntoOneRealBuild is COORD-002's
// other required test: two different projects that happen to resolve
// the identical dependency+version concurrently must join one real
// build, not race to produce two competing publications — the RWMutex
// alone permits both through (COORD-001's own doc note on this), so
// this proves the keyed singleflight coordination actually engages at
// the real syncVersion level too, not just in coordinator_test.go's
// synthetic fn.
func TestSameDependencyAcrossProjectsCoalescesIntoOneRealBuild(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()
	f := newCrossProjectTestFixture(t)

	repo := atomicPromotionFixtureRepo(t)
	reg := atomicPromotionTestRegistry(t, repo)
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}

	blocked := newBarrierEmbedder(4)
	actionFromA := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_a", Dependency: dep}
	actionFromB := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_b", Dependency: dep}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- f.coordinator.Build(dep, func() error {
			return syncVersion(ctx, f.store, f.badgerStore, f.gitCache, blocked, f.vb, f.ns, reg, actionFromA, false)
		})
	}()

	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first build never reached its embed step")
	}

	// A second, concurrent request for the identical dependency+version
	// — from a different project — must join the first in-flight build
	// rather than starting its own (a second real syncVersion call would
	// itself block forever on blocked.release only once, since the fake
	// embedder is shared; if it ran independently this call would hang
	// past the timeout below instead of joining).
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- f.coordinator.Build(dep, func() error {
			return syncVersion(ctx, f.store, f.badgerStore, f.gitCache, blocked, f.vb, f.ns, reg, actionFromB, false)
		})
	}()

	// No hook exists to know deterministically when the second caller has
	// actually reached group.Do and registered as a joiner (same
	// limitation as coordinator_test.go's own coalescing test) — release
	// too early and it could miss joining, start its own independent
	// build, and call blocked.Embed a second time, panicking on the
	// already-closed started channel. That failure mode itself would
	// mean coalescing broke, so this sleep is a confidence margin, not a
	// correctness requirement the test's pass/fail depends on silently.
	time.Sleep(50 * time.Millisecond)
	close(blocked.release)

	for i, ch := range []chan error{firstDone, secondDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("caller %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d never completed — the second request did not join the first's in-flight build", i)
		}
	}

	active, err := f.store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", f.vb.Name())
	if err != nil || active.State != domain.GenActive {
		t.Fatalf("generation after both Build calls = %+v, err=%v, want exactly one ACTIVE generation", active, err)
	}
}

// TestSyncVersionPhaseTimingsReflectRealDelay proves onSyncPhaseTimings
// (COORD-002's other required instrumentation, alongside
// BuildCoordinator's GateWait/Work split) actually measures each real
// phase, not just returns zeros: a deliberate delay inside the embedder
// shows up specifically as Replicate time, not smeared across every
// phase or attributed to the wrong one.
func TestSyncVersionPhaseTimingsReflectRealDelay(t *testing.T) {
	requireGitForAtomicPromotionTest(t)
	ctx := context.Background()
	f := newCrossProjectTestFixture(t)

	repo := atomicPromotionFixtureRepo(t)
	reg := atomicPromotionTestRegistry(t, repo)
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}
	action := planner.Action{Kind: planner.ActionSyncVersion, ProjectID: "proj_timing", Dependency: dep}

	const delay = 1500 * time.Millisecond
	slow := &delayedEmbedder{atomicPromotionFakeEmbedder: atomicPromotionFakeEmbedder{dims: 4}, delay: delay}

	var mu sync.Mutex
	var got SyncPhaseTimings
	var calls int
	originalHook := onSyncPhaseTimings
	onSyncPhaseTimings = func(d domain.DependencyVersion, t SyncPhaseTimings) {
		mu.Lock()
		defer mu.Unlock()
		got = t
		calls++
	}
	t.Cleanup(func() { onSyncPhaseTimings = originalHook })

	if err := syncVersion(ctx, f.store, f.badgerStore, f.gitCache, slow, f.vb, f.ns, reg, action, false); err != nil {
		t.Fatalf("syncVersion: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("onSyncPhaseTimings called %d times, want 1", calls)
	}
	if got.Replicate < delay {
		t.Errorf("Replicate = %v, want at least the injected %v embed delay", got.Replicate, delay)
	}
	if got.Build >= delay {
		t.Errorf("Build = %v, want well under the %v embed delay — the delay is only inside Replicate's embed call", got.Build, delay)
	}
	// validate.VersionCorrectness also calls Embed once (see
	// barrierEmbedder's own comment above), and delayedEmbedder delays
	// every call — so Validate legitimately picks up ~one delay too,
	// same order of magnitude as Replicate. What would indicate phases
	// bleeding into each other (a real bug) is Validate reflecting
	// *two* delays' worth — as if it were somehow also counting
	// Replicate's own wait — not one.
	if got.Validate < delay {
		t.Errorf("Validate = %v, want at least ~%v (its own real Embed call)", got.Validate, delay)
	}
	if got.Validate > 2*delay {
		t.Errorf("Validate = %v, want under ~2x the injected delay — looks like it absorbed Replicate's own wait too", got.Validate)
	}
	if got.Promote <= 0 {
		t.Errorf("Promote = %v, want a real positive duration", got.Promote)
	}
}

// delayedEmbedder wraps atomicPromotionFakeEmbedder with a fixed,
// unconditional delay on every Embed call — a simpler tool than
// barrierEmbedder's channel-based barrier when the test just needs a
// real, measurable, bounded duration rather than external control over
// exactly when the call unblocks.
type delayedEmbedder struct {
	atomicPromotionFakeEmbedder
	delay time.Duration
}

func (d *delayedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	time.Sleep(d.delay)
	return d.atomicPromotionFakeEmbedder.Embed(ctx, texts)
}

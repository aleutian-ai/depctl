package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/watch"
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

// loopHarness runs a changeLoop against a hand-fed event channel.
type loopHarness struct {
	events chan watch.ChangeEvent
	log    syncBuffer
	cancel context.CancelFunc
	done   chan struct{}
}

func startLoop(t *testing.T, handle func(context.Context, watch.ChangeEvent) error) *loopHarness {
	t.Helper()
	h := &loopHarness{events: make(chan watch.ChangeEvent), done: make(chan struct{})}
	loop := &changeLoop{
		events:       h.events,
		handle:       handle,
		refresh:      func(context.Context) {},
		logf:         func(format string, args ...any) { fmt.Fprintf(&h.log, format+"\n", args...) },
		retryAfter:   10 * time.Millisecond,
		refreshEvery: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		loop.run(ctx)
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	return h
}

func TestChangeLoopContinuesAfterFailedChange(t *testing.T) {
	var mu sync.Mutex
	var handled []string
	h := startLoop(t, func(ctx context.Context, ev watch.ChangeEvent) error {
		mu.Lock()
		handled = append(handled, ev.ProjectID)
		mu.Unlock()
		if ev.ProjectID == "proj_bad" {
			return errors.New("resolve failed")
		}
		return nil
	})

	h.events <- watch.ChangeEvent{ProjectID: "proj_bad"}
	h.events <- watch.ChangeEvent{ProjectID: "proj_good"}
	waitFor(t, "both changes handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 2
	})
	if !strings.Contains(h.log.String(), "proj_bad: resolve failed") {
		t.Errorf("log = %q, want the failure reported", h.log.String())
	}
}

func TestChangeLoopRetriesChangeBlockedByLock(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	h := startLoop(t, func(ctx context.Context, ev watch.ChangeEvent) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return fmt.Errorf("open control store: %w", bboltstore.ErrLocked)
		}
		return nil
	})

	h.events <- watch.ChangeEvent{ProjectID: "proj_a"}
	waitFor(t, "the locked change to be retried", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return attempts == 2
	})
	if !strings.Contains(h.log.String(), "retrying in") {
		t.Errorf("log = %q, want a retry notice", h.log.String())
	}
}

func TestChangeLoopFinishesInFlightChangeOnCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handlerCtxErr := make(chan error, 1)
	h := startLoop(t, func(ctx context.Context, ev watch.ChangeEvent) error {
		close(started)
		<-release
		handlerCtxErr <- ctx.Err()
		return nil
	})

	h.events <- watch.ChangeEvent{ProjectID: "proj_a"}
	<-started
	h.cancel()
	close(release)

	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit after the in-flight change finished")
	}
	if err := <-handlerCtxErr; err != nil {
		t.Errorf("in-flight change saw ctx.Err() = %v, want nil (it must be allowed to finish)", err)
	}
}

// deadEndpointsConfig points embedding and vector at nothing, so any
// SYNC_VERSION work fails fast instead of reaching a real local service.
func deadEndpointsConfig(t *testing.T, mutate func(*config.Config)) {
	t.Helper()
	dead := deadBackendURL(t)
	writeTestConfig(t, func(c *config.Config) {
		c.Embedding.Endpoint = dead
		c.Vector.Endpoint = dead
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

func TestResyncProjectPicksUpNewDependency(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	deadEndpointsConfig(t, nil)
	root := scanDepFixture(t)
	id := projectIDForRoot(t, root)
	addBarDependency(t, root)

	cfg, err := loadRagctlConfig()
	if err != nil {
		t.Fatalf("loadRagctlConfig: %v", err)
	}
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	badgerStore, err := openDataStore()
	if err != nil {
		t.Fatalf("openDataStore: %v", err)
	}
	defer badgerStore.Close()

	var out bytes.Buffer
	if err := resyncProject(context.Background(), store, badgerStore, cfg, id, &out); err != nil {
		t.Fatalf("resyncProject: %v\n%s", err, out.String())
	}

	res, err := store.GetResolution(context.Background(), id)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	var names []string
	for _, d := range res.Dependencies {
		names = append(names, d.Dependency.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "example.com/bar") {
		t.Errorf("resolution after resync = %v, want example.com/bar", names)
	}

	refs, err := store.ListAllReferences(context.Background())
	if err != nil {
		t.Fatalf("ListAllReferences: %v", err)
	}
	var barRef bool
	for _, r := range refs {
		barRef = barRef || (r.ProjectID == id && r.Package == "example.com/bar")
	}
	if !barRef {
		t.Errorf("references after resync = %+v, want one for example.com/bar (plan ran and recorded it)", refs)
	}
}

func TestWatchRefusesWhenDisabled(t *testing.T) {
	isolateEnv(t)
	writeTestConfig(t, func(c *config.Config) { c.Watch.Enabled = false })

	root := NewRootCmd()
	root.SetOut(new(bytes.Buffer))
	root.SetArgs([]string{"watch"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "watch disabled") {
		t.Fatalf("watch with watch.enabled=false = %v, want a 'watch disabled' error", err)
	}
}

func TestWatchFailsClearlyWhenStoreLocked(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	holder, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer holder.Close()

	root := NewRootCmd()
	root.SetOut(new(bytes.Buffer))
	root.SetArgs([]string{"watch"})
	if err := root.Execute(); !errors.Is(err, bboltstore.ErrLocked) {
		t.Fatalf("watch with the store locked = %v, want ErrLocked", err)
	}
}

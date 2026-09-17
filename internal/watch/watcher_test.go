package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/domain"
)

const testDebounce = 50 * time.Millisecond

// quietPeriod is how long a test waits to conclude no event is coming:
// several debounce windows, so a real event would have fired by then.
const quietPeriod = 6 * testDebounce

type testLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *testLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func startWatcher(t *testing.T, projects ...Project) (*Watcher, *testLog) {
	t.Helper()
	log := &testLog{}
	w, err := New(testDebounce, log.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.SetProjects(projects)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return w, log
}

func goProject(t *testing.T, id string) Project {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/"+id+"\n")
	return Project{ID: id, Root: root, Ecosystem: domain.EcosystemGo}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func nextEvent(t *testing.T, w *Watcher) ChangeEvent {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a change event")
		return ChangeEvent{}
	}
}

func expectNoEvent(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(quietPeriod):
	}
}

func TestWatchPathsOnlyExistingManifests(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/x\n")
	writeFile(t, filepath.Join(root, "README.md"), "# x\n")

	got := WatchPaths(Project{Root: root, Ecosystem: domain.EcosystemGo})
	if want := []string{filepath.Join(root, "go.mod")}; !reflect.DeepEqual(got, want) {
		t.Errorf("WatchPaths = %v, want %v (go.sum absent, README not a manifest)", got, want)
	}

	writeFile(t, filepath.Join(root, "go.sum"), "")
	if got := WatchPaths(Project{Root: root, Ecosystem: domain.EcosystemGo}); len(got) != 2 {
		t.Errorf("WatchPaths after creating go.sum = %v, want go.mod and go.sum", got)
	}
}

func TestWatchPathsUnknownEcosystemIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Cargo.toml"), "")
	if got := WatchPaths(Project{Root: root, Ecosystem: domain.EcosystemRust}); got != nil {
		t.Errorf("WatchPaths for an ecosystem without a resolver = %v, want nil", got)
	}
}

func TestRapidWritesCoalesceIntoOneEvent(t *testing.T) {
	p := goProject(t, "proj_a")
	w, _ := startWatcher(t, p)

	for i := range 5 {
		writeFile(t, filepath.Join(p.Root, "go.mod"), fmt.Sprintf("module example.com/a // %d\n", i))
	}

	ev := nextEvent(t, w)
	if ev.ProjectID != "proj_a" || !reflect.DeepEqual(ev.Paths, []string{filepath.Join(p.Root, "go.mod")}) {
		t.Errorf("event = %+v, want proj_a with go.mod", ev)
	}
	expectNoEvent(t, w)
}

func TestRenameIntoPlaceTriggersAndWatchSurvives(t *testing.T) {
	p := goProject(t, "proj_a")
	w, _ := startWatcher(t, p)

	tmp := filepath.Join(p.Root, "go.sum.tmp")
	writeFile(t, tmp, "example.com/dep v1.0.0 h1:abc=\n")
	if err := os.Rename(tmp, filepath.Join(p.Root, "go.sum")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if ev := nextEvent(t, w); !reflect.DeepEqual(ev.Paths, []string{filepath.Join(p.Root, "go.sum")}) {
		t.Errorf("rename-into-place event = %+v, want only go.sum (the temp file isn't a manifest)", ev)
	}

	writeFile(t, filepath.Join(p.Root, "go.sum"), "example.com/dep v1.1.0 h1:def=\n")
	if ev := nextEvent(t, w); ev.ProjectID != "proj_a" {
		t.Errorf("write after rename = %+v, want another proj_a event", ev)
	}
}

func TestRemoveThenRecreateIsOneEvent(t *testing.T) {
	p := goProject(t, "proj_a")
	w, _ := startWatcher(t, p)

	path := filepath.Join(p.Root, "go.mod")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeFile(t, path, "module example.com/a\n\ngo 1.25\n")

	if ev := nextEvent(t, w); ev.ProjectID != "proj_a" {
		t.Errorf("event = %+v, want proj_a", ev)
	}
	expectNoEvent(t, w)
}

func TestNonManifestChangesAreIgnored(t *testing.T) {
	p := goProject(t, "proj_a")
	w, _ := startWatcher(t, p)

	writeFile(t, filepath.Join(p.Root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(p.Root, "package.json"), "{}\n")
	expectNoEvent(t, w)

	writeFile(t, filepath.Join(p.Root, "go.mod"), "module example.com/a // edited\n")
	if ev := nextEvent(t, w); ev.ProjectID != "proj_a" {
		t.Errorf("event = %+v, want proj_a", ev)
	}
}

func TestRemovedProjectStopsEmitting(t *testing.T) {
	a := goProject(t, "proj_a")
	b := goProject(t, "proj_b")
	w, _ := startWatcher(t, a, b)

	w.SetProjects([]Project{b})
	writeFile(t, filepath.Join(a.Root, "go.mod"), "module example.com/a // edited\n")
	expectNoEvent(t, w)

	writeFile(t, filepath.Join(b.Root, "go.mod"), "module example.com/b // edited\n")
	if ev := nextEvent(t, w); ev.ProjectID != "proj_b" {
		t.Errorf("event = %+v, want proj_b", ev)
	}
}

func TestSlowConsumerDoesNotBlockWatching(t *testing.T) {
	a := goProject(t, "proj_a")
	b := goProject(t, "proj_b")
	w, _ := startWatcher(t, a, b)

	writeFile(t, filepath.Join(a.Root, "go.mod"), "module example.com/a // edited\n")
	time.Sleep(quietPeriod) // proj_a's event is now blocked waiting for a reader
	writeFile(t, filepath.Join(b.Root, "go.mod"), "module example.com/b // edited\n")
	time.Sleep(quietPeriod)

	got := map[string]bool{nextEvent(t, w).ProjectID: true, nextEvent(t, w).ProjectID: true}
	if !got["proj_a"] || !got["proj_b"] {
		t.Errorf("events = %v, want both proj_a and proj_b", got)
	}
}

func TestUnwatchableRootIsSkipped(t *testing.T) {
	good := goProject(t, "proj_good")
	missing := Project{ID: "proj_missing", Root: filepath.Join(t.TempDir(), "gone"), Ecosystem: domain.EcosystemGo}
	w, log := startWatcher(t, missing, good)

	if !strings.Contains(log.String(), "cannot watch") {
		t.Errorf("log = %q, want a warning for the missing root", log.String())
	}
	writeFile(t, filepath.Join(good.Root, "go.mod"), "module example.com/good // edited\n")
	if ev := nextEvent(t, w); ev.ProjectID != "proj_good" {
		t.Errorf("event = %+v, want proj_good", ev)
	}
}

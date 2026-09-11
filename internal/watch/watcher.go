package watch

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ChangeEvent reports that one or more of a project's manifest files
// changed, after the debounce window closed with no further changes.
type ChangeEvent struct {
	ProjectID string
	Paths     []string
}

// Watcher turns filesystem events under project roots into debounced,
// per-project ChangeEvents.
//
// It watches each project's root directory rather than the manifest files
// themselves: package managers and editors commonly replace a lockfile by
// renaming a temp file over it, which silently ends a watch held on the
// old file. A directory watch survives that, coalesces remove-then-create
// into one change, and sees a lockfile created for the first time. fsnotify
// directory watches are not recursive, and events are filtered to manifest
// names, so nothing else in the project tree is watched or reported.
type Watcher struct {
	fsw      *fsnotify.Watcher
	debounce time.Duration
	logf     func(format string, args ...any)
	events   chan ChangeEvent
	done     chan struct{}

	mu      sync.Mutex
	byRoot  map[string]Project
	pending map[string]*burst
}

// burst is one project's changes accumulating inside a debounce window.
type burst struct {
	timer *time.Timer
	paths map[string]bool
}

// New returns a Watcher with nothing watched yet. logf receives warnings
// (a root that can't be watched, fsnotify errors, recovered panics).
func New(debounce time.Duration, logf func(format string, args ...any)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create filesystem watcher: %w", err)
	}
	return &Watcher{
		fsw:      fsw,
		debounce: debounce,
		logf:     logf,
		events:   make(chan ChangeEvent),
		done:     make(chan struct{}),
		byRoot:   map[string]Project{},
		pending:  map[string]*burst{},
	}, nil
}

// Events delivers one ChangeEvent per debounced burst of changes. Sends
// happen off the filesystem-event goroutine, so a slow consumer delays
// delivery but never stops the Watcher from observing further changes.
func (w *Watcher) Events() <-chan ChangeEvent {
	return w.events
}

// SetProjects makes projects the complete watched set: new roots are
// added, and roots no longer present are unwatched with any pending
// change for them dropped. A root that can't be watched is logged and
// skipped; the rest are still watched.
func (w *Watcher) SetProjects(projects []Project) {
	want := make(map[string]Project, len(projects))
	for _, p := range projects {
		want[filepath.Clean(p.Root)] = p
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for root, p := range w.byRoot {
		if _, keep := want[root]; keep {
			continue
		}
		if err := w.fsw.Remove(root); err != nil {
			w.logf("stop watching %s: %v", root, err)
		}
		delete(w.byRoot, root)
		w.dropPendingLocked(p.ID)
	}

	for root, p := range want {
		if _, watched := w.byRoot[root]; !watched {
			if err := w.fsw.Add(root); err != nil {
				w.logf("cannot watch %s: %v", root, err)
				continue
			}
		}
		w.byRoot[root] = p
	}
}

// Run processes filesystem events until ctx is done, then releases the
// underlying watcher and unblocks any pending Events send.
func (w *Watcher) Run(ctx context.Context) {
	defer w.shutdown()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.logf("filesystem watcher: %v", err)
		}
	}
}

// handle only records the change and (re)arms the project's debounce
// timer; everything downstream happens on the timer's goroutine.
func (w *Watcher) handle(ev fsnotify.Event) {
	defer func() {
		if r := recover(); r != nil {
			w.logf("recovered while handling %s: %v", ev.Name, r)
		}
	}()
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Remove) && !ev.Has(fsnotify.Rename) {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	p, ok := w.byRoot[filepath.Dir(ev.Name)]
	if !ok || !isManifest(p.Ecosystem, filepath.Base(ev.Name)) {
		return
	}
	if b := w.pending[p.ID]; b != nil {
		b.paths[ev.Name] = true
		b.timer.Reset(w.debounce)
		return
	}
	b := &burst{paths: map[string]bool{ev.Name: true}}
	b.timer = time.AfterFunc(w.debounce, func() { w.fire(p.ID, b) })
	w.pending[p.ID] = b
}

// fire emits b's accumulated change. It does nothing if b is no longer
// the project's current burst: the project was removed, or a timer re-armed
// by a late event fired after b was already emitted.
func (w *Watcher) fire(projectID string, b *burst) {
	w.mu.Lock()
	if w.pending[projectID] != b {
		w.mu.Unlock()
		return
	}
	delete(w.pending, projectID)
	paths := make([]string, 0, len(b.paths))
	for path := range b.paths {
		paths = append(paths, path)
	}
	w.mu.Unlock()

	sort.Strings(paths)
	select {
	case w.events <- ChangeEvent{ProjectID: projectID, Paths: paths}:
	case <-w.done:
	}
}

func (w *Watcher) dropPendingLocked(projectID string) {
	if b := w.pending[projectID]; b != nil {
		b.timer.Stop()
	}
	delete(w.pending, projectID)
}

func (w *Watcher) shutdown() {
	w.mu.Lock()
	for id := range w.pending {
		w.dropPendingLocked(id)
	}
	w.mu.Unlock()
	close(w.done)
	if err := w.fsw.Close(); err != nil {
		w.logf("close filesystem watcher: %v", err)
	}
}

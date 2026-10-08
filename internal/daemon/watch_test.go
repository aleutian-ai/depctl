package daemon

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aleutian-ai/depctl/internal/daemon/api"
	"github.com/aleutian-ai/depctl/internal/watch"
)

type projectListEngine struct {
	Engine
	mu       sync.Mutex
	projects []watch.Project
}

func (e *projectListEngine) set(projects ...watch.Project) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.projects = projects
}

func (e *projectListEngine) Projects(context.Context) ([]watch.Project, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]watch.Project(nil), e.projects...), nil
}

// TestFirstSightOfANewProjectStartsAFullSync is SCOPE-002: a project that
// appears after the daemon's initial load gets a full sync with no
// operator step; projects already registered at startup, and projects
// seen again on later refreshes, do not.
func TestFirstSightOfANewProjectStartsAFullSync(t *testing.T) {
	var mu sync.Mutex
	var synced []string
	var sawResolve bool
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		mu.Lock()
		defer mu.Unlock()
		synced = append(synced, projectID)
		sawResolve = sawResolve || opts.Resolve
		if len(opts.Dependencies) != 0 {
			t.Errorf("first-sight sync for %s was scoped to %v, want a full sync", projectID, opts.Dependencies)
		}
		return api.SyncResult{ProjectID: projectID}, nil
	}
	ids := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), synced...)
	}

	eng := &projectListEngine{}
	s := New(Options{Engine: eng, Socket: "unused-in-this-test", Logf: func(string, ...any) {}})
	s.scheduler = NewScheduler(context.Background(), run, noGC, nil)
	w, err := watch.New(time.Millisecond, func(string, ...any) {})
	if err != nil {
		t.Fatalf("watch.New: %v", err)
	}
	s.watcher = w
	ctx := context.Background()

	// Already registered when the daemon starts: watched, not synced.
	eng.set(watch.Project{ID: "proj_old", Root: t.TempDir()})
	s.refreshProjects(ctx)
	s.scheduler.Wait()
	if got := ids(); len(got) != 0 {
		t.Fatalf("startup load synced %v, want nothing (a restart is not a first registration)", got)
	}

	// A new registration appears: exactly it gets a full sync.
	eng.set(watch.Project{ID: "proj_old", Root: t.TempDir()}, watch.Project{ID: "proj_new", Root: t.TempDir()})
	s.refreshProjects(ctx)
	s.scheduler.Wait()
	if got := ids(); len(got) != 1 || got[0] != "proj_new" {
		t.Fatalf("after registering proj_new, synced %v, want exactly [proj_new]", got)
	}
	mu.Lock()
	if !sawResolve {
		t.Error("first-sight sync did not re-resolve; a project visible before its resolution is persisted would sync nothing")
	}
	mu.Unlock()

	// Seen again on later refreshes: no duplicate trigger.
	s.refreshProjects(ctx)
	s.refreshProjects(ctx)
	s.scheduler.Wait()
	if got := ids(); len(got) != 1 {
		t.Errorf("re-seeing known projects synced %v, want no further syncs", got)
	}
}

// TestDisableAmbientSyncSuppressesFirstSightSync: the opt-out for CI and
// tests that want only explicit or just-in-time syncs.
func TestDisableAmbientSyncSuppressesFirstSightSync(t *testing.T) {
	var mu sync.Mutex
	var synced []string
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, _ SyncOptions, _ io.Writer) (api.SyncResult, error) {
		mu.Lock()
		defer mu.Unlock()
		synced = append(synced, projectID)
		return api.SyncResult{ProjectID: projectID}, nil
	}
	eng := &projectListEngine{}
	s := New(Options{Engine: eng, Socket: "unused-in-this-test", Logf: func(string, ...any) {}, DisableAmbientSync: true})
	s.scheduler = NewScheduler(context.Background(), run, noGC, nil)
	w, err := watch.New(time.Millisecond, func(string, ...any) {})
	if err != nil {
		t.Fatalf("watch.New: %v", err)
	}
	s.watcher = w

	s.refreshProjects(context.Background())
	eng.set(watch.Project{ID: "proj_new", Root: t.TempDir()})
	s.refreshProjects(context.Background())
	s.scheduler.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(synced) != 0 {
		t.Errorf("synced %v with ambient sync disabled, want nothing", synced)
	}
}

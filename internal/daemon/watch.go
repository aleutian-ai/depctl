package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/aleutian-ai/depctl/internal/watch"
)

// projectRefreshInterval is how often the daemon re-reads the registered
// project list, so projects added or removed while it runs are picked up
// without a restart. It also refreshes right after any request that
// changes the set.
const projectRefreshInterval = time.Minute

// startWatch begins watching every registered project's dependency
// manifests. A change marks the project dirty through the scheduler;
// nothing blocking runs on the filesystem-event goroutine.
func (s *Server) startWatch(ctx context.Context) error {
	w, err := watch.New(s.opts.Debounce, s.opts.Logf)
	if err != nil {
		return err
	}
	s.watcher = w
	s.refreshProjects(ctx)

	go w.Run(ctx)
	go s.watchLoop(ctx)
	go s.refreshLoop(ctx)
	return nil
}

// watchLoop turns debounced manifest changes into sync requests. It
// doesn't wait for the sync: the scheduler collapses repeated changes
// into one follow-up run on its own.
func (s *Server) watchLoop(ctx context.Context) {
	events := s.watcher.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.opts.Logf("change in %s: %s", s.rootOf(ev.ProjectID), baseNames(ev.Paths))
			s.scheduler.Request(ev.ProjectID, SyncOptions{Resolve: true}, s.opts.Out)
		}
	}
}

func (s *Server) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(projectRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshProjects(ctx)
		}
	}
}

// refreshProjects hands the current project set to the watcher, logging
// what started or stopped being watched.
func (s *Server) refreshProjects(ctx context.Context) {
	if s.watcher == nil {
		return
	}
	projects, err := s.opts.Engine.Projects(ctx)
	if err != nil {
		s.opts.Logf("refresh project list: %v", err)
		return
	}

	var firstSight []watch.Project
	s.watchedMu.Lock()
	next := make(map[string]watch.Project, len(projects))
	for _, p := range projects {
		next[p.ID] = p
		if _, ok := s.watched[p.ID]; !ok {
			s.opts.Logf("watching %s (%s)", p.Root, baseNames(watch.WatchPaths(p)))
			if s.watchSeeded && !s.opts.DisableAmbientSync {
				firstSight = append(firstSight, p)
			}
		}
	}
	for id, p := range s.watched {
		if _, ok := next[id]; !ok {
			s.opts.Logf("stopped watching %s (no longer registered)", p.Root)
		}
	}
	s.watcher.SetProjects(projects)
	s.watched = next
	s.watchSeeded = true
	s.watchedMu.Unlock()

	// A full sync starts on first registration (SCOPE-002) — the
	// deliberate default, not something an operator has to remember. Only
	// registrations that appear after the daemon's initial load count: at
	// startup every already-registered project is new to the watcher, and
	// re-syncing the whole fleet on each restart is not "first".
	// Resolve is set because the project can become visible here before
	// its resolution is persisted, and syncing an unresolved project
	// silently does nothing.
	for _, p := range firstSight {
		s.opts.Logf("first sight of %s: starting a full sync", p.Root)
		s.scheduler.Request(p.ID, SyncOptions{Resolve: true}, s.opts.Out)
	}
}

// rootOf returns a watched project's root, for log lines.
func (s *Server) rootOf(projectID string) string {
	s.watchedMu.Lock()
	defer s.watchedMu.Unlock()
	return s.watched[projectID].Root
}

func baseNames(paths []string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return strings.Join(names, ", ")
}

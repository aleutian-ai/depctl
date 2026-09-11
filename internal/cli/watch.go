package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/watch"
)

const (
	// watchRefreshInterval is how often watch re-reads the registered
	// project list, so projects added by `ragctl scan` (or removed) while
	// it runs are picked up without a restart.
	watchRefreshInterval = time.Minute

	// watchLockRetry is how long watch waits before retrying a change it
	// couldn't process because another ragctl process (typically `ragctl
	// serve`) held the control database lock.
	watchLockRetry = 30 * time.Second
)

// changeLoop consumes debounced change events one at a time. Syncs run
// here, never on the watcher's filesystem-event goroutine, and they run
// sequentially, so two changes never sync concurrently.
type changeLoop struct {
	events       <-chan watch.ChangeEvent
	handle       func(context.Context, watch.ChangeEvent) error
	refresh      func(context.Context)
	logf         func(format string, args ...any)
	retryAfter   time.Duration
	refreshEvery time.Duration
}

// lockedWriter serializes writes from the loop and the watcher's own
// goroutines onto one output stream.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// run processes events until ctx is done. A change already being handled
// when ctx is cancelled runs to completion: it gets a context that isn't
// cancelled with ctx, so a Ctrl-C never abandons a half-built generation.
func (l *changeLoop) run(ctx context.Context) {
	retry := make(chan watch.ChangeEvent)
	ticker := time.NewTicker(l.refreshEvery)
	defer ticker.Stop()

	process := func(ev watch.ChangeEvent) {
		err := l.handle(context.WithoutCancel(ctx), ev)
		switch {
		case errors.Is(err, bboltstore.ErrLocked):
			l.logf("%s: %v; retrying in %s", ev.ProjectID, err, l.retryAfter)
			time.AfterFunc(l.retryAfter, func() {
				select {
				case retry <- ev:
				case <-ctx.Done():
				}
			})
		case err != nil:
			l.logf("%s: %v", ev.ProjectID, err)
		}
		l.refresh(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-l.events:
			process(ev)
		case ev := <-retry:
			process(ev)
		case <-ticker.C:
			l.refresh(ctx)
		}
	}
}

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Watch projects for dependency changes",
		Long: `Watch every registered project's dependency manifests (go.mod,
package-lock.json, uv.lock, ...) and, when one changes, re-resolve that
project and sync it, exactly as ` + "`ragctl sync --project <id>`" + ` would.

Runs in the foreground until interrupted. Ctrl-C lets a sync already in
progress finish before exiting; press it again to stop immediately.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd)
		},
	}
}

func runWatch(cmd *cobra.Command) error {
	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}
	if !cfg.Watch.Enabled {
		return fmt.Errorf("watch disabled (watch.enabled: false in config)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	out := &lockedWriter{w: cmd.OutOrStdout()}
	logf := func(format string, args ...any) {
		fmt.Fprintf(out, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
	}

	projects, err := loadWatchProjects(context.Background())
	if err != nil {
		return err
	}
	w, err := watch.New(cfg.Watch.Debounce, logf)
	if err != nil {
		return err
	}
	watched := map[string]watch.Project{}
	applyWatchProjects(w, watched, projects, logf)

	watcherDone := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(watcherDone)
	}()
	go func() {
		<-ctx.Done()
		stop() // restore default signal handling, so a second Ctrl-C exits immediately
	}()

	logf("watching %d project(s), debounce %s; Ctrl-C to stop", len(watched), cfg.Watch.Debounce)

	loop := &changeLoop{
		events: w.Events(),
		handle: func(ctx context.Context, ev watch.ChangeEvent) error {
			logf("change in %s: %s", watched[ev.ProjectID].Root, baseNames(ev.Paths))
			return syncChangedProject(ctx, cfg, ev.ProjectID, out)
		},
		refresh: func(ctx context.Context) {
			projects, err := loadWatchProjects(ctx)
			if errors.Is(err, bboltstore.ErrLocked) {
				return
			}
			if err != nil {
				logf("refresh project list: %v", err)
				return
			}
			applyWatchProjects(w, watched, projects, logf)
		},
		logf:         logf,
		retryAfter:   watchLockRetry,
		refreshEvery: watchRefreshInterval,
	}
	loop.run(ctx)

	<-watcherDone
	logf("stopped")
	return nil
}

// loadWatchProjects returns every registered project that has been
// resolved; its ecosystem comes from the stored resolution. The control
// store is opened only for this read, so an idle watch never holds the
// lock other commands need.
func loadWatchProjects(ctx context.Context) ([]watch.Project, error) {
	store, err := openControlStore()
	if err != nil {
		return nil, fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	projects, err := store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var out []watch.Project
	for _, p := range projects {
		res, err := store.GetResolution(ctx, p.ID)
		if errors.Is(err, bboltstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get resolution for %s: %w", p.ID, err)
		}
		out = append(out, watch.Project{ID: p.ID, Root: p.Root, Ecosystem: res.Ecosystem})
	}
	return out, nil
}

// applyWatchProjects hands the full set to the watcher and logs what
// started or stopped being watched since the last call.
func applyWatchProjects(w *watch.Watcher, watched map[string]watch.Project, projects []watch.Project, logf func(string, ...any)) {
	next := make(map[string]watch.Project, len(projects))
	for _, p := range projects {
		next[p.ID] = p
		if _, ok := watched[p.ID]; !ok {
			logf("watching %s (%s)", p.Root, baseNames(watch.WatchPaths(p)))
		}
	}
	for id, p := range watched {
		if _, ok := next[id]; !ok {
			logf("stopped watching %s (no longer registered)", p.Root)
		}
	}
	w.SetProjects(projects)
	clear(watched)
	for id, p := range next {
		watched[id] = p
	}
}

// syncChangedProject opens the stores for the duration of one change.
func syncChangedProject(ctx context.Context, cfg config.Config, projectID string, out io.Writer) error {
	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	badgerStore, err := openDataStore()
	if err != nil {
		return fmt.Errorf("open data store: %w", err)
	}
	defer badgerStore.Close()

	return resyncProject(ctx, store, badgerStore, cfg, projectID, out)
}

// resyncProject re-resolves one project, stores the new resolution, and
// runs the same plan-and-sync `ragctl sync --project` does.
func resyncProject(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, projectID string, out io.Writer) error {
	p, err := store.GetProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	prev, err := store.GetResolution(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get previous resolution: %w", err)
	}
	r, ok := resolvers[prev.Ecosystem]
	if !ok {
		return fmt.Errorf("no resolver for ecosystem %q", prev.Ecosystem)
	}

	res, err := r.Resolve(ctx, p.Root)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", p.Root, err)
	}
	if err := store.PutResolution(ctx, projectID, res); err != nil {
		return fmt.Errorf("store resolution: %w", err)
	}
	fmt.Fprintf(out, "resolved %s: %d dependencies\n", p.Root, len(res.Dependencies))

	_, _, _, err = RunSync(ctx, store, badgerStore, cfg, projectID, "", false, false, out)
	return err
}

func baseNames(paths []string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return strings.Join(names, ", ")
}

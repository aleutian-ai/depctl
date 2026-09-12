// Package daemon runs the one process that owns ragctl's persistent
// stores. Every other ragctl process — CLI commands, `ragctl serve`,
// and the watcher — reaches that state through this package's HTTP/JSON
// API over a Unix domain socket, never by opening bbolt or Badger
// itself. See docs/adr/ADR-011-single-owner-daemon.md.
//
// This package owns the transport and lifecycle only. The work behind
// each route is done by an Engine, implemented in internal/cli over the
// already-open stores, so no domain logic moves here.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
	"aleutian-ai/ragctl/internal/watch"
)

// shutdownGrace bounds how long a graceful shutdown waits for in-flight
// requests before connections are closed outright.
const shutdownGrace = 30 * time.Second

// Engine is the daemon's view of ragctl's actual functionality. It is
// implemented in internal/cli against the stores the daemon holds open;
// defining it here (consumer-side) keeps internal/daemon free of bbolt,
// Badger, and config types, and avoids an import cycle with the CLI.
type Engine interface {
	Status(ctx context.Context) (api.Status, error)
	Projects(ctx context.Context) ([]watch.Project, error)
	ProjectIDs(ctx context.Context) ([]string, error)
	Sync(ctx context.Context, projectID string, opts SyncOptions, out io.Writer) (api.SyncResult, error)
	// Scan discovers projects under root and persists each one's
	// registration and resolution. lockProject must be held around one
	// project's persist step (see Scheduler.LockProject) so two
	// concurrent scans that discover the same project can't race each
	// other's writes.
	Scan(ctx context.Context, root string, out io.Writer, lockProject func(projectID string) func()) ([]string, error)
	Plan(ctx context.Context, projectID string) (any, error)
	GC(ctx context.Context, dryRun bool, out io.Writer) (api.GCResult, error)

	// Search, ProjectDependencies, DependencyVersion, ReleaseChanges, and
	// KnowledgeStatus back the MCP query tools (internal/mcp.QueryService),
	// reached via ragctl serve's daemonQueryService (ADR-011 §8). All
	// read-only; none touch the scheduler.
	Search(ctx context.Context, req api.SearchRequest) (api.SearchResponse, error)
	ProjectDependencies(ctx context.Context, projectID string) (api.ProjectDependenciesResponse, error)
	DependencyVersion(ctx context.Context, projectID, pkg string) (api.DependencyVersionResponse, error)
	ReleaseChanges(ctx context.Context, dependency, from, to string) (api.ReleaseChangesResponse, error)
	KnowledgeStatus(ctx context.Context) (api.KnowledgeStatusResponse, error)

	// ProjectList, ProjectGet, and Describe back `ragctl project`,
	// `ragctl deps`, and `ragctl describe` — all read-only.
	ProjectList(ctx context.Context) (api.ProjectListResponse, error)
	ProjectGet(ctx context.Context, projectID string) (api.ProjectGetResponse, error)
	Describe(ctx context.Context, args []string, checkLiveness bool) (any, error)

	// Doctor runs every check that needs the stores this daemon holds
	// open — everything except the two PATH lookups a client must run
	// against its own shell PATH regardless (see api.DoctorResponse).
	// Deliberately not reached via ensureDaemon's autostart: `doctor`
	// dials without spawning, since diagnosing a stopped or broken
	// daemon is its job — see internal/cli/doctor.go's runDoctor.
	Doctor(ctx context.Context) (api.DoctorResponse, error)
}

// Options configures a Server. Socket and Engine are required.
type Options struct {
	Engine         Engine
	Socket         string
	ControlPath    string
	Version        string
	WatchEnabled   bool
	Debounce       time.Duration
	MCPEnabled     bool
	EnableSyncTool bool
	// ConfigFingerprint is config.Config.Fingerprint() for the config
	// this daemon loaded at startup — see api.Health.ConfigFingerprint.
	ConfigFingerprint string
	Logf              func(format string, args ...any)
	// Out receives progress from syncs the daemon starts itself (watch),
	// as opposed to those streamed back to a waiting client.
	Out io.Writer
}

// Server serves the daemon API on a Unix socket.
type Server struct {
	opts      Options
	started   time.Time
	stop      chan struct{}
	stopOnce  sync.Once
	scheduler *Scheduler

	watcher   *watch.Watcher
	watchedMu sync.Mutex
	watched   map[string]watch.Project
}

// New returns a Server ready to Serve. It binds nothing yet.
func New(opts Options) *Server {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	return &Server{opts: opts, stop: make(chan struct{}), watched: map[string]watch.Project{}}
}

// Serve binds the socket and serves until ctx is cancelled or Shutdown
// is called, then shuts down gracefully. The caller must already own the
// stores, which is what proves any socket file found here is stale.
func (s *Server) Serve(ctx context.Context) error {
	s.started = time.Now()

	// Watching and syncing stop when Serve does, whether that was a
	// signal or a shutdown request.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.scheduler = NewScheduler(runCtx, s.opts.Engine.Sync, s.opts.Engine.GC, s.opts.Logf)

	// The caller holds the control store's lock, so no live daemon can be
	// using this path: anything here is left over from a crash.
	if err := os.Remove(s.opts.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket %s: %w", s.opts.Socket, err)
	}
	ln, err := net.Listen("unix", s.opts.Socket)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.opts.Socket, err)
	}
	if err := os.Chmod(s.opts.Socket, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("restrict socket %s: %w", s.opts.Socket, err)
	}

	if s.opts.WatchEnabled {
		if err := s.startWatch(runCtx); err != nil {
			ln.Close()
			return fmt.Errorf("start watching: %w", err)
		}
		s.opts.Logf("watching for dependency changes, debounce %s", s.opts.Debounce)
	} else {
		s.opts.Logf("watching disabled (watch.enabled: false in config)")
	}

	srv := &http.Server{Handler: s.routes()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case <-s.stop:
		}
		cancel()
		grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(grace); err != nil {
			srv.Close()
		}
	}()

	s.opts.Logf("ragctl daemon listening on %s (pid %d)", s.opts.Socket, os.Getpid())
	err = srv.Serve(ln)
	<-done

	// A sync already under way finishes; anything queued behind it is
	// dropped and resynced after the next change or command.
	s.scheduler.Shutdown()
	s.scheduler.Wait()

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown asks a running Serve to stop; it returns immediately.
func (s *Server) Shutdown() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+api.PathHealth, s.handleHealth)
	mux.HandleFunc("GET "+api.PathStatus, s.handleStatus)
	mux.HandleFunc("POST "+api.PathShutdown, s.handleShutdown)
	mux.HandleFunc("POST "+api.PathResolve, s.handleResolve)
	mux.HandleFunc("POST "+api.PathPlan, s.handlePlan)
	mux.HandleFunc("POST "+api.PathSync, s.handleSync)
	mux.HandleFunc("POST "+api.PathGC, s.handleGC)
	mux.HandleFunc("POST "+api.PathSearch, s.handleSearch)
	mux.HandleFunc("POST "+api.PathProjectDependencies, s.handleProjectDependencies)
	mux.HandleFunc("POST "+api.PathDependencyVersion, s.handleDependencyVersion)
	mux.HandleFunc("POST "+api.PathReleaseChanges, s.handleReleaseChanges)
	mux.HandleFunc("POST "+api.PathKnowledgeStatus, s.handleKnowledgeStatus)
	mux.HandleFunc("POST "+api.PathProjectList, s.handleProjectList)
	mux.HandleFunc("POST "+api.PathProjectGet, s.handleProjectGet)
	mux.HandleFunc("POST "+api.PathDescribe, s.handleDescribe)
	mux.HandleFunc("POST "+api.PathDoctor, s.handleDoctor)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Health{
		PID:               os.Getpid(),
		StartedAt:         s.started,
		Socket:            s.opts.Socket,
		ControlPath:       s.opts.ControlPath,
		Version:           s.opts.Version,
		Watching:          s.watcher != nil,
		MCPEnabled:        s.opts.MCPEnabled,
		EnableSyncTool:    s.opts.EnableSyncTool,
		ConfigFingerprint: s.opts.ConfigFingerprint,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.opts.Engine.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	st.GCRunning = s.gcBusy()
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleShutdown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, struct{}{})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.opts.Logf("shutdown requested")
	s.Shutdown()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, api.Error{Error: err.Error()})
}

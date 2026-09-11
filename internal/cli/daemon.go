package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon"
	"aleutian-ai/ragctl/internal/daemon/api"
	"aleutian-ai/ragctl/internal/daemon/client"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/watch"
)

// ragctlVersion is reported by the daemon over /v1/health.
const ragctlVersion = "v0.1.0"

// stopWait bounds how long `ragctl daemon stop` waits for the socket to
// go away after the daemon accepts the request.
const stopWait = 30 * time.Second

const (
	// autostartTimeout bounds how long a client waits for a daemon it
	// started itself to come up.
	autostartTimeout = 5 * time.Second

	// autostartPoll is how often the client retries the socket while
	// waiting.
	autostartPoll = 100 * time.Millisecond
)

// daemonExecutable is the binary auto-start spawns. It is empty in
// normal use (meaning "this binary"); tests point it at a real ragctl
// they built, since os.Executable under `go test` is the test binary.
var daemonExecutable string

// ensureDaemon returns a client for the running daemon, starting one if
// none is running and config allows it. Every command that needs stored
// state goes through here: there is deliberately no path that opens the
// stores directly instead (ADR-011).
func ensureDaemon(ctx context.Context) (*client.Client, error) {
	socket, err := socketPath()
	if err != nil {
		return nil, err
	}
	c, err := client.Dial(ctx, socket)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, client.ErrNotRunning) {
		return nil, err
	}

	cfg, err := loadRagctlConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.Daemon.AutostartEnabled() {
		return nil, notRunningError(socket)
	}
	if err := requireInitialized(); err != nil {
		return nil, err
	}
	if err := spawnDaemon(); err != nil {
		return nil, err
	}
	return waitForDaemon(ctx, socket)
}

// requireNoDaemon fails if a daemon is running, for the few commands
// that open the stores directly and so can't share them.
func requireNoDaemon(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	socket, err := socketPath()
	if err != nil {
		return err
	}
	c, err := client.Dial(ctx, socket)
	if err != nil {
		return nil // not running, or unreachable: nothing owns the stores
	}
	h, err := c.Health(ctx)
	if err != nil {
		return nil
	}
	return fmt.Errorf("the ragctl daemon is running (pid %d); stop it first with `ragctl daemon stop`", h.PID)
}

// notRunningError is what a command reports when there is no daemon and
// it may not start one.
func notRunningError(socket string) error {
	return fmt.Errorf("%w (socket %s); start it with `ragctl daemon run`, or set daemon.autostart: true in config",
		client.ErrNotRunning, socket)
}

// requireInitialized refuses to auto-start a daemon that would fail
// immediately because `ragctl init` hasn't run.
func requireInitialized() error {
	controlPath, err := controlDBPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(controlPath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ragctl is not initialized (%s does not exist); run `ragctl init` first", controlPath)
	}
	return nil
}

// spawnDaemon starts `ragctl daemon run` detached. Its output goes to
// ragctld.log, never to the caller's stdout: the caller may be `ragctl
// serve`, whose stdout carries the MCP protocol stream.
func spawnDaemon() error {
	exe := daemonExecutable
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("locate the ragctl binary: %w", err)
		}
	}
	logPath, err := daemonLogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon", "run")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ragctl daemon: %w", err)
	}
	return cmd.Process.Release()
}

// waitForDaemon polls the socket until the daemon answers, reporting
// what the daemon logged if it never does.
func waitForDaemon(ctx context.Context, socket string) (*client.Client, error) {
	deadline := time.Now().Add(autostartTimeout)
	for {
		c, err := client.Dial(ctx, socket)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, client.ErrNotRunning) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("ragctl daemon did not start within %s; last log lines:\n%s", autostartTimeout, tailDaemonLog())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(autostartPoll):
		}
	}
}

// tailDaemonLog returns the last few lines of ragctld.log, for errors
// that report why an auto-started daemon never came up.
func tailDaemonLog() string {
	path, err := daemonLogPath()
	if err != nil {
		return "(no log available)"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(could not read %s: %v)", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return strings.Join(lines, "\n")
}

// engine implements daemon.Engine over the stores the daemon holds open
// for its lifetime. Every handler's work happens through here, against
// the same functions the CLI used to call directly.
type engine struct {
	store       *bboltstore.Store
	badgerStore *badgerstore.Store
	cfg         config.Config
	controlPath string
	badgerPath  string
}

// Status returns the `ragctl status` snapshot, including a live backend
// health probe.
func (e *engine) Status(ctx context.Context) (api.Status, error) {
	st, err := buildStatus(ctx, e.store, e.cfg.Vector.Backend, e.controlPath, e.badgerPath)
	if err != nil {
		return api.Status{}, err
	}
	st.Backend = api.BackendStatus{Name: e.cfg.Vector.Backend, Healthy: probeBackend(ctx, e.cfg) == nil}
	return st, nil
}

// Projects returns the projects the daemon watches: every registered
// project with a stored resolution.
func (e *engine) Projects(ctx context.Context) ([]watch.Project, error) {
	return watchProjects(ctx, e.store)
}

// Sync runs one project's sync, re-resolving first when the request came
// from a manifest change. It is the function the scheduler runs, and it
// calls exactly the same RunSync as `ragctl sync`.
func (e *engine) Sync(ctx context.Context, projectID string, opts daemon.SyncOptions, out io.Writer) (api.SyncResult, error) {
	if opts.Resolve {
		if err := resolveProject(ctx, e.store, projectID, out); err != nil {
			return api.SyncResult{}, err
		}
	}
	synced, failed, skipped, err := RunSync(ctx, e.store, e.badgerStore, e.cfg, projectID, opts.Dependency, opts.Offline, opts.Force, out)
	return api.SyncResult{ProjectID: projectID, Synced: synced, Failed: failed, Skipped: skipped}, err
}

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the ragctl daemon",
		Long: `The ragctl daemon owns ragctl's databases. Every command that needs
stored state talks to it over a local socket, and starts one if none is
running (unless daemon.autostart is false in config).`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "run",
			Short: "Run the ragctl daemon in the foreground",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return runDaemonRun(cmd) },
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the ragctl daemon is running",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return runDaemonStatus(cmd) },
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the running ragctl daemon",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return runDaemonStop(cmd) },
		},
	)
	return cmd
}

func runDaemonRun(cmd *cobra.Command) error {
	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}
	socket, err := socketPath()
	if err != nil {
		return err
	}
	controlPath, err := controlDBPath()
	if err != nil {
		return err
	}
	badgerPath, err := badgerDirPath()
	if err != nil {
		return err
	}

	// Ownership of the control store, not the socket file, decides who is
	// the daemon (ADR-011): a crash leaves a socket behind, but never the
	// lock.
	store, err := openControlStore()
	if err != nil {
		if errors.Is(err, bboltstore.ErrLocked) {
			return ownershipError(cmd.Context(), socket, controlPath)
		}
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	badgerStore, err := openDataStore()
	if err != nil {
		return fmt.Errorf("open data store: %w", err)
	}
	defer badgerStore.Close()

	out := &lockedWriter{w: cmd.OutOrStdout()}
	logf := func(format string, args ...any) {
		fmt.Fprintf(out, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default handling, so a second Ctrl-C exits immediately
	}()

	srv := daemon.New(daemon.Options{
		Engine:         &engine{store: store, badgerStore: badgerStore, cfg: cfg, controlPath: controlPath, badgerPath: badgerPath},
		Socket:         socket,
		ControlPath:    controlPath,
		Version:        ragctlVersion,
		WatchEnabled:   cfg.Watch.Enabled,
		Debounce:       cfg.Watch.Debounce,
		MCPEnabled:     cfg.Server.MCP.Enabled,
		EnableSyncTool: cfg.Server.MCP.EnableSyncTool,
		Logf:           logf,
		Out:            out,
	})
	if err := srv.Serve(ctx); err != nil {
		return err
	}
	logf("stopped")
	return nil
}

// ownershipError explains a locked control store: either another daemon
// is already running (the normal outcome of two commands auto-starting
// at once), or something else holds the lock and nothing answers.
func ownershipError(ctx context.Context, socket, controlPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if h, err := client.Dial(probe, socket); err == nil {
		health, err := h.Health(probe)
		if err == nil {
			return fmt.Errorf("ragctl daemon already running (pid %d, socket %s)", health.PID, socket)
		}
	}
	return fmt.Errorf("%s is locked but no daemon answers on %s; another ragctl process holds it", controlPath, socket)
}

func runDaemonStatus(cmd *cobra.Command) error {
	socket, err := socketPath()
	if err != nil {
		return err
	}
	c, err := client.Dial(cmd.Context(), socket)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			fmt.Fprintf(cmd.OutOrStdout(), "ragctl daemon is not running (socket %s)\n", socket)
			return ExitCodeError{Code: 1}
		}
		return err
	}
	h, err := c.Health(cmd.Context())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%-14s running (pid %d)\n", "daemon:", h.PID)
	fmt.Fprintf(out, "%-14s %s\n", "socket:", h.Socket)
	fmt.Fprintf(out, "%-14s %s\n", "control db:", h.ControlPath)
	fmt.Fprintf(out, "%-14s %s\n", "version:", h.Version)
	fmt.Fprintf(out, "%-14s %s\n", "uptime:", time.Since(h.StartedAt).Truncate(time.Second))
	fmt.Fprintf(out, "%-14s %t\n", "watching:", h.Watching)
	return nil
}

func runDaemonStop(cmd *cobra.Command) error {
	socket, err := socketPath()
	if err != nil {
		return err
	}
	c, err := client.Dial(cmd.Context(), socket)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			fmt.Fprintln(cmd.OutOrStdout(), "ragctl daemon is not running")
			return nil
		}
		return err
	}
	if err := c.Shutdown(cmd.Context()); err != nil {
		return err
	}

	deadline := time.Now().Add(stopWait)
	for time.Now().Before(deadline) {
		if _, err := client.Dial(cmd.Context(), socket); errors.Is(err, client.ErrNotRunning) {
			fmt.Fprintln(cmd.OutOrStdout(), "stopped")
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not stop within %s", stopWait)
}

// lockedWriter serializes writes from the daemon's goroutines onto one
// output stream.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

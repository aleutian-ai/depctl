// Package client talks to the local ragctl daemon over its Unix socket.
// Every ragctl command that needs persistent state goes through here
// instead of opening bbolt or Badger itself (ADR-011).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// ErrNotRunning means nothing is listening on the daemon socket: the
// socket file is absent, or it's stale and refuses connections.
var ErrNotRunning = errors.New("ragctl daemon is not running")

// Request timeouts. c.http itself sets none (unlike http.DefaultClient,
// which also sets none — the exact gap found and fixed in the qdrant
// client, one layer down, this session): every public method below
// wraps its own ctx with one of these, so a stuck daemon-side call hangs
// the one calling command for a bounded time, never forever. vars only
// so tests can shorten them.
var (
	// defaultRequestTimeout covers everything except the streamed,
	// long-running calls and Describe. Comfortably above any single
	// downstream dependency's own bound (the ollama/qdrant clients cap
	// themselves at 60s), so a legitimately slow-but-working call still
	// succeeds.
	defaultRequestTimeout = 90 * time.Second

	// describeRequestTimeout is more generous: `ragctl describe
	// --check-liveness` can run many bounded (10s each,
	// registry.livenessTimeout) but numerous liveness probes across a
	// large registered fleet.
	describeRequestTimeout = 10 * time.Minute

	// longRunningRequestTimeout bounds Resolve/Sync/GC, the streamed
	// calls that legitimately run long. Set a safety margin above the
	// daemon's own ceiling (maxActionDuration, internal/daemon/
	// scheduler.go) rather than leaving the client with no bound at
	// all: the server already promises to finish or fail within
	// maxActionDuration, so this is belt-and-suspenders against that
	// promise ever being broken by a bug, not a limit expected to bind
	// in practice. Resolve (scan) has no server-side ceiling of its own
	// yet, so it shares this generous bound too rather than the default.
	longRunningRequestTimeout = 35 * time.Minute
)

// Client is a connection to one daemon socket. It is safe for
// concurrent use.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a Client for the socket path without contacting it.
func New(socket string) *Client {
	return &Client{
		socket: socket,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// Dial returns a Client only if a daemon answers on the socket,
// otherwise ErrNotRunning.
func Dial(ctx context.Context, socket string) (*Client, error) {
	c := New(socket)
	if _, err := c.Health(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Socket returns the socket path this client dials.
func (c *Client) Socket() string { return c.socket }

// Health reports the running daemon's identity and loaded config.
func (c *Client) Health(ctx context.Context) (api.Health, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var h api.Health
	err := c.do(ctx, http.MethodGet, api.PathHealth, nil, &h)
	return h, err
}

// Status returns the `ragctl status` snapshot.
func (c *Client) Status(ctx context.Context) (api.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var st api.Status
	err := c.do(ctx, http.MethodGet, api.PathStatus, nil, &st)
	return st, err
}

// Shutdown asks the daemon to stop; it returns as soon as the request is
// accepted, not when the daemon has exited.
func (c *Client) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, api.PathShutdown, nil, nil)
}

// Resolve runs `ragctl scan`'s work for root in the daemon, relaying
// progress to out.
func (c *Client) Resolve(ctx context.Context, root string, out io.Writer) (api.ResolveResult, error) {
	ctx, cancel := context.WithTimeout(ctx, longRunningRequestTimeout)
	defer cancel()
	var res api.ResolveResult
	err := c.stream(ctx, api.PathResolve, api.ResolveRequest{Root: root}, out, &res)
	return res, err
}

// Sync runs a sync in the daemon, relaying progress to out.
func (c *Client) Sync(ctx context.Context, req api.SyncRequest, out io.Writer) (api.SyncResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, longRunningRequestTimeout)
	defer cancel()
	var res api.SyncResponse
	err := c.stream(ctx, api.PathSync, req, out, &res)
	return res, err
}

// Plan decodes the desired-state plan into plans, which is the CLI's own
// plan type: the wire format is whatever that marshals to.
func (c *Client) Plan(ctx context.Context, projectID string, plans any) error {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, api.PathPlan, api.PlanRequest{ProjectID: projectID}, plans)
}

// GC runs garbage collection in the daemon, relaying progress to out.
func (c *Client) GC(ctx context.Context, dryRun bool, out io.Writer) (api.GCResult, error) {
	ctx, cancel := context.WithTimeout(ctx, longRunningRequestTimeout)
	defer cancel()
	var res api.GCResult
	err := c.stream(ctx, api.PathGC, api.GCRequest{DryRun: dryRun}, out, &res)
	return res, err
}

// Search runs a knowledge search in the daemon, the work behind the
// search_dependency_docs MCP tool.
func (c *Client) Search(ctx context.Context, req api.SearchRequest) (api.SearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.SearchResponse
	err := c.do(ctx, http.MethodPost, api.PathSearch, req, &res)
	return res, err
}

// ProjectDependencies lists a project's resolved dependencies, the work
// behind the list_project_dependencies MCP tool.
func (c *Client) ProjectDependencies(ctx context.Context, projectID string) (api.ProjectDependenciesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.ProjectDependenciesResponse
	err := c.do(ctx, http.MethodPost, api.PathProjectDependencies, api.ProjectDependenciesRequest{ProjectID: projectID}, &res)
	return res, err
}

// DependencyVersion resolves one package's version within a project, the
// work behind the get_dependency_version MCP tool.
func (c *Client) DependencyVersion(ctx context.Context, projectID, pkg string) (api.DependencyVersionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.DependencyVersionResponse
	err := c.do(ctx, http.MethodPost, api.PathDependencyVersion, api.DependencyVersionRequest{ProjectID: projectID, Package: pkg}, &res)
	return res, err
}

// ReleaseChanges gets release-note excerpts between two versions of a
// dependency, the work behind the get_release_changes MCP tool.
func (c *Client) ReleaseChanges(ctx context.Context, dependency, from, to string) (api.ReleaseChangesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.ReleaseChangesResponse
	err := c.do(ctx, http.MethodPost, api.PathReleaseChanges, api.ReleaseChangesRequest{Dependency: dependency, From: from, To: to}, &res)
	return res, err
}

// KnowledgeStatus summarizes fleet-wide sync coverage, the work behind
// the knowledge_status MCP tool. Not to be confused with Status, the
// daemon/process health snapshot.
func (c *Client) KnowledgeStatus(ctx context.Context) (api.KnowledgeStatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.KnowledgeStatusResponse
	err := c.do(ctx, http.MethodPost, api.PathKnowledgeStatus, nil, &res)
	return res, err
}

// ProjectList lists every registered project, the work behind
// `ragctl project list`.
func (c *Client) ProjectList(ctx context.Context) (api.ProjectListResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.ProjectListResponse
	err := c.do(ctx, http.MethodPost, api.PathProjectList, nil, &res)
	return res, err
}

// ProjectGet gets one project's full detail, the work behind
// `ragctl project show` and `ragctl deps`.
func (c *Client) ProjectGet(ctx context.Context, projectID string) (api.ProjectGetResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.ProjectGetResponse
	err := c.do(ctx, http.MethodPost, api.PathProjectGet, api.ProjectGetRequest{ProjectID: projectID}, &res)
	return res, err
}

// Describe decodes `ragctl describe`'s report into report, which is the
// CLI's own Report type: the wire format is whatever that marshals to
// (the same pattern Plan uses for its own CLI-side type).
func (c *Client) Describe(ctx context.Context, args []string, checkLiveness bool, report any) error {
	ctx, cancel := context.WithTimeout(ctx, describeRequestTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, api.PathDescribe, api.DescribeRequest{Args: args, CheckLiveness: checkLiveness}, report)
}

// Doctor runs every doctor check that needs the daemon's stores. Not
// reached through ensureDaemon's autostart — see api.PathDoctor.
func (c *Client) Doctor(ctx context.Context) (api.DoctorResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	var res api.DoctorResponse
	err := c.do(ctx, http.MethodPost, api.PathDoctor, nil, &res)
	return res, err
}

// stream posts a request whose response is NDJSON: progress lines are
// copied to out as they arrive, and the final line carries the result or
// the error.
func (c *Client) stream(ctx context.Context, path string, body any, out io.Writer, result any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://ragctl"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		if notRunning(err) {
			return fmt.Errorf("%w (socket %s)", ErrNotRunning, c.socket)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return responseError(resp)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var line api.StreamLine
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("daemon closed the connection before finishing %s", path)
			}
			return fmt.Errorf("decode %s stream: %w", path, err)
		}
		switch {
		case line.Error != "":
			return errors.New(line.Error)
		case line.Result != nil:
			if result == nil {
				return nil
			}
			if err := json.Unmarshal(line.Result, result); err != nil {
				return fmt.Errorf("decode %s result: %w", path, err)
			}
			return nil
		default:
			if out != nil {
				fmt.Fprintln(out, line.Log)
			}
		}
	}
}

// do sends one request, decoding a JSON body into out when out is
// non-nil. A connection failure becomes ErrNotRunning, so callers can
// tell "no daemon" from "the daemon said no".
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	// The host is ignored — the transport always dials c.socket — but
	// net/http requires a syntactically valid URL.
	req, err := http.NewRequestWithContext(ctx, method, "http://ragctl"+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if notRunning(err) {
			return fmt.Errorf("%w (socket %s)", ErrNotRunning, c.socket)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return responseError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// notRunning reports whether err means nothing is listening, as opposed
// to a daemon that answered with a failure.
func notRunning(err error) bool {
	return errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET)
}

// responseError turns a non-2xx response into the daemon's own error
// message where it sent one.
func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e api.Error
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	if len(bytes.TrimSpace(body)) > 0 {
		return fmt.Errorf("daemon returned %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return fmt.Errorf("daemon returned %s", resp.Status)
}

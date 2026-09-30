package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/mcp"
	"aleutian-ai/ragctl/internal/query"
)

// blockingScanTrigger is a mcp.ScanTrigger whose ScanProject blocks until
// unblock is closed, signaling started first — used by
// TestServeMCPDoesNotBlockOnSlowStartupScan (MCP-009) to prove the MCP
// loop becomes usable while a slow scan is still genuinely in flight.
type blockingScanTrigger struct {
	started chan struct{}
	unblock chan struct{}
}

func (b *blockingScanTrigger) ScanProject(ctx context.Context, root string, progress func(line string)) ([]string, string, error) {
	close(b.started)
	<-b.unblock
	return nil, "", nil
}

// fakeStartupScanTrigger and fakeStartupQueryService are minimal
// mcp.ScanTrigger/mcp.QueryService fakes scoped to what startupScan
// itself calls (Status only) — everything else panics if exercised,
// since startupScan never should.

type fakeStartupScanTrigger struct {
	ids     []string
	summary string
	err     error
}

func (f *fakeStartupScanTrigger) ScanProject(ctx context.Context, root string, progress func(line string)) ([]string, string, error) {
	return f.ids, f.summary, f.err
}

type fakeStartupQueryService struct {
	status query.Status
	err    error
}

func (f *fakeStartupQueryService) Status(ctx context.Context) (query.Status, error) {
	return f.status, f.err
}
func (f *fakeStartupQueryService) GetProjectDependencies(ctx context.Context, projectID string) ([]query.ProjectDependency, error) {
	panic("not used by startupScan")
}
func (f *fakeStartupQueryService) GetDependencyVersion(ctx context.Context, projectID, pkg string) (domain.DependencyVersion, error) {
	panic("not used by startupScan")
}
func (f *fakeStartupQueryService) GetReleaseChanges(ctx context.Context, dependency, from, to string) ([]query.ReleaseChange, error) {
	panic("not used by startupScan")
}
func (f *fakeStartupQueryService) SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error) {
	panic("not used by startupScan")
}

func TestStartupScanNilTrigger(t *testing.T) {
	var buf bytes.Buffer
	startupScan(context.Background(), &buf, nil, nil)
	if buf.Len() != 0 {
		t.Errorf("nil ScanTrigger: got output %q, want none", buf.String())
	}
}

func TestStartupScanFailureIsNonFatal(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{err: errors.New("boom")}
	startupScan(context.Background(), &buf, scan, &fakeStartupQueryService{})
	if !strings.Contains(buf.String(), "startup scan failed") || !strings.Contains(buf.String(), "boom") {
		t.Errorf("scan error output = %q, want a logged, non-fatal message naming the error", buf.String())
	}
}

func TestStartupScanNoProjectsSkipsStatus(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{ids: nil, summary: "no projects found under .\n"}
	q := &fakeStartupQueryService{err: errors.New("Status must not be called with zero projects")}
	startupScan(context.Background(), &buf, scan, q)
	if !strings.Contains(buf.String(), "no projects found") {
		t.Errorf("output = %q, want the scan summary printed", buf.String())
	}
	if strings.Contains(buf.String(), "must not be called") {
		t.Errorf("startupScan called Status() despite zero registered project IDs: %q", buf.String())
	}
}

func TestStartupScanReportsStatusSummary(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{ids: []string{"proj_1"}, summary: "registered proj_1\n"}
	q := &fakeStartupQueryService{status: query.Status{
		TotalProjects: 1, TotalDependencies: 42, WithActiveGeneration: 10, WithoutActiveGeneration: 32,
	}}
	startupScan(context.Background(), &buf, scan, q)
	out := buf.String()
	if !strings.Contains(out, "registered proj_1") {
		t.Errorf("output = %q, want the scan summary printed", out)
	}
	if !strings.Contains(out, "1 project(s), 42 dependencies, 10/42 already synced") {
		t.Errorf("output = %q, want a status summary with the fake's numbers", out)
	}
}

func TestStartupScanStatusFailureIsNonFatal(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{ids: []string{"proj_1"}}
	q := &fakeStartupQueryService{err: errors.New("daemon unreachable")}
	startupScan(context.Background(), &buf, scan, q)
	if !strings.Contains(buf.String(), "startup status check failed") || !strings.Contains(buf.String(), "daemon unreachable") {
		t.Errorf("status error output = %q, want a logged, non-fatal message naming the error", buf.String())
	}
}

// TestServeMCPDoesNotBlockOnSlowStartupScan is MCP-009's regression test:
// confirmed live against a real, never-before-scanned dependency-heavy Go
// project that a cold-cache first-time resolution (`go list -m -json all`
// hitting the network) used to block the entire MCP handshake, since
// startupScan ran synchronously before the MCP loop started at all — a
// connecting client had no way to tell the server was even alive. This
// proves the fix structurally: with a ScanProject call genuinely still in
// flight (blocked on an unclosed channel, not just fast), a real MCP
// client over a real (in-memory) transport can already connect and call a
// tool successfully.
func TestServeMCPDoesNotBlockOnSlowStartupScan(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	scan := &blockingScanTrigger{started: started, unblock: unblock}
	deps := mcp.Deps{Query: &fakeStartupQueryService{}, Scan: scan}

	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- serveMCP(ctx, deps, io.Discard, serverTransport) }()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("startupScan never started")
	}

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "knowledge_status"})
	if err != nil {
		t.Fatalf("CallTool(knowledge_status) while startupScan still blocked: %v", err)
	}
	if result.IsError {
		t.Fatalf("knowledge_status returned a tool-level error while startupScan still blocked: %v", result.Content)
	}

	close(unblock)
	cancel()
	if err := <-serveErr; err != nil && ctx.Err() == nil {
		t.Errorf("serveMCP returned unexpected error: %v", err)
	}
}

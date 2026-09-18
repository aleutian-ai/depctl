package cli

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// VERIFY-001: a real ragctl serve subprocess, spawned over stdio, driven
// by a real MCP client — the exact shape of session that caught
// WATCH-019/020's sentinel-identity bug and GRAPH-003's module-path bug,
// neither of which the existing sdkmcp.NewInMemoryTransports()-based
// internal/mcp tests could have caught (those never leave the process,
// so a daemon-HTTP wire bug is invisible to them). No live embedder or
// vector backend — scoped to calls that don't need one (see the
// ticket's own non-goals).

// callToolText calls tool with args against session and returns the
// concatenated text content, failing the test on a transport error or a
// tool-level error result (name the error text, don't just fail blind).
func callToolText(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	text := sb.String()
	if result.IsError {
		t.Fatalf("CallTool(%s) returned a tool-level error: %s", tool, text)
	}
	return text
}

// callToolExpectError is callToolText's counterpart for calls this test
// expects to fail — it fails the test if the call unexpectedly
// succeeds, and returns the error/result text either way for the caller
// to inspect.
func callToolExpectError(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return err.Error()
	}
	var sb strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	text := sb.String()
	if !result.IsError {
		t.Fatalf("CallTool(%s) succeeded, want a tool-level error: %s", tool, text)
	}
	return text
}

func TestServeOverRealStdioTransport(t *testing.T) {
	isolateEnv(t)
	requireGo(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	root := scanDepFixture(t) // registers example.com/app depending on example.com/foo (local replace)
	writeFile(t, root, "main.go", `package main

import "fmt"

func main() {
	helper()
	fmt.Println("done")
}

func helper() {}
`)

	bin := requireRagctlBinary(t)
	cmd := exec.Command(bin, "serve")
	cmd.Dir = root

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "verify-001", Version: "0.0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to real ragctl serve subprocess: %v", err)
	}
	defer session.Close()

	// knowledge_status: the one project scanDepFixture registered.
	statusText := callToolText(t, ctx, session, "knowledge_status", nil)
	var status struct {
		TotalProjects int `json:"total_projects"`
		Projects      []struct {
			ProjectID string `json:"project_id"`
			Root      string `json:"root"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(statusText), &status); err != nil {
		t.Fatalf("decode knowledge_status: %v\nraw: %s", err, statusText)
	}
	if status.TotalProjects != 1 || len(status.Projects) != 1 {
		t.Fatalf("knowledge_status = %+v, want exactly one registered project", status)
	}
	projectID := status.Projects[0].ProjectID
	if projectID == "" {
		t.Fatalf("knowledge_status projects entry has no project_id: %s", statusText)
	}

	// list_project_dependencies: the one dependency scanDepFixture resolved.
	depsText := callToolText(t, ctx, session, "list_project_dependencies", map[string]any{"project_id": projectID})
	if !strings.Contains(depsText, "example.com/foo") {
		t.Errorf("list_project_dependencies missing example.com/foo:\n%s", depsText)
	}

	// get_dependency_version: real daemon-HTTP round trip for a known package.
	verText := callToolText(t, ctx, session, "get_dependency_version", map[string]any{"project_id": projectID, "package": "example.com/foo"})
	if !strings.Contains(verText, "example.com/foo") {
		t.Errorf("get_dependency_version missing example.com/foo:\n%s", verText)
	}

	// scan_project: always safe to re-run, per its own tool description.
	rescanText := callToolText(t, ctx, session, "scan_project", nil)
	if !strings.Contains(rescanText, projectID) {
		t.Errorf("scan_project re-run result missing the already-registered project_id %s:\n%s", projectID, rescanText)
	}

	// search_dependency_docs against the unsynced example.com/foo: must
	// be a tool-level error carrying toolError's ErrNoActiveGeneration
	// wording — this exact path is WATCH-019/020's own sentinel-identity
	// bug, now covered by a real subprocess round trip on every push.
	searchErrText := callToolExpectError(t, ctx, session, "search_dependency_docs", map[string]any{
		"project_id": projectID, "query": "anything", "dependency": "example.com/foo",
	})
	if !strings.Contains(searchErrText, "no synced knowledge") {
		t.Errorf("search_dependency_docs error text = %q, want the toolError-mapped ErrNoActiveGeneration message", searchErrText)
	}

	// explain_call_site against an internal call site (helper(), line 6):
	// no error, an explanatory note instead.
	internalText := callToolText(t, ctx, session, "explain_call_site", map[string]any{
		"project_id": projectID, "file": "main.go", "line": 6, "column": 2,
	})
	if !strings.Contains(internalText, "inside the project") {
		t.Errorf("explain_call_site for an internal call site = %q, want the internal-call-site note", internalText)
	}

	// explain_call_site against a stdlib call (fmt.Println, line 7):
	// resolves as an external symbol, then fails to match anything in
	// the project's resolved dependencies (ragctl doesn't track the
	// standard library as a dependency) — a tool-level error naming
	// that, not a panic or a hang. This exact join (external-symbol
	// resolution -> matched-dependency lookup) is where GRAPH-003's
	// module-path bug lived.
	stdlibErrText := callToolExpectError(t, ctx, session, "explain_call_site", map[string]any{
		"project_id": projectID, "file": "main.go", "line": 7, "column": 6,
	})
	if !strings.Contains(stdlibErrText, "resolved dependencies") {
		t.Errorf("explain_call_site for a stdlib call site = %q, want the ErrDependencyNotResolved-mapped message", stdlibErrText)
	}
}

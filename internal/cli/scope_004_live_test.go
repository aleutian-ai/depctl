package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPrioritizeFileAndExplainCallSiteOverARealDaemon is epic 54's own
// closing live verification: SCOPE-003 (multi-dependency scoped sync)
// and SCOPE-004 (prioritize_file, explain_call_site's JIT wiring) were
// both built and unit/integration tested against fakes, but — per both
// tickets' own post-implementation notes — never exercised against a
// real daemon and a real agent client. This is that exercise: a real
// `ragctl serve` subprocess, a real MCP client over stdio (matching
// VERIFY-001's own established shape), and two genuinely different real
// Go modules so prioritize_file's *set* mechanism (not just a single
// dependency) gets exercised for real. Needs real network, a running
// Ollama, and a running Qdrant — same live-dependency shape as VALID-003's
// benchmark, same opt-in gate.
func TestPrioritizeFileAndExplainCallSiteOverARealDaemon(t *testing.T) {
	if os.Getenv("RAGCTL_LIVE_BENCHMARK") == "" {
		t.Skip("set RAGCTL_LIVE_BENCHMARK=1 to run this live verification (needs network, a running Ollama, and a running Qdrant on 127.0.0.1:6333)")
	}
	isolateEnv(t)
	noAmbientSync(t) // this test drives sync explicitly via the MCP tools under test
	requireGo(t)
	t.Setenv("GOPROXY", "https://proxy.golang.org,direct")
	runInitForTest(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	writeGoMod(t, root, "module example.com/scope004app\n\ngo 1.21\n")
	writeFile(t, root, "main.go", `package main

import (
	"fmt"

	"github.com/google/go-cmp/cmp"
	flag "github.com/spf13/pflag"
)

func main() {
	flag.String("name", "world", "name to greet")
	flag.Parse()
	fmt.Println(cmp.Equal(1, 1))
}
`)
	for _, mod := range []string{"github.com/spf13/pflag", "github.com/google/go-cmp/cmp"} {
		getCmd := exec.Command("go", "get", mod+"@latest")
		getCmd.Dir = root
		getCmd.Env = os.Environ()
		if out, err := getCmd.CombinedOutput(); err != nil {
			t.Skipf("go get %s@latest failed (likely offline): %v\n%s", mod, err, out)
		}
	}
	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = root
	tidyCmd.Env = os.Environ()
	if out, err := tidyCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}

	scanCmd := NewRootCmd()
	scanCmd.SetArgs([]string{"scan", root})
	if err := scanCmd.Execute(); err != nil {
		t.Skipf("scan failed (likely offline or module resolution issue): %v", err)
	}

	bin := requireRagctlBinary(t)
	cmd := exec.Command(bin, "serve")
	cmd.Dir = root

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "scope-004-live", Version: "0.0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to real ragctl serve subprocess: %v", err)
	}
	defer session.Close()

	statusText := callToolText(t, ctx, session, "knowledge_status", nil)
	var status struct {
		Projects []struct {
			ProjectID string `json:"project_id"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(statusText), &status); err != nil {
		t.Fatalf("decode knowledge_status: %v\nraw: %s", err, statusText)
	}
	if len(status.Projects) != 1 {
		t.Fatalf("knowledge_status = %s, want exactly one registered project", statusText)
	}
	projectID := status.Projects[0].ProjectID

	// Step 1: explain_call_site at pflag.String(...) (line 12) — this
	// dependency has never been synced. SCOPE-004's own JIT wiring must
	// trigger a real sync for just this one dependency and either return
	// real evidence or a bounded still_building — never the old dead end.
	t.Log("explain_call_site: triggering a fresh JIT sync for pflag")
	explainText := callToolText(t, ctx, session, "explain_call_site", map[string]any{
		"project_id": projectID, "file": "main.go", "line": 12, "column": 7,
	})
	if strings.Contains(explainText, "still_building") {
		t.Logf("explain_call_site reported still_building (bounded wait exceeded) — acceptable, not a dead end: %s", explainText)
	} else if !strings.Contains(explainText, "pflag") {
		t.Errorf("explain_call_site real evidence = %q, want it naming pflag", explainText)
	} else {
		t.Logf("explain_call_site returned real evidence: %s", truncateForLog(explainText))
	}

	// Step 2: prioritize_file on the whole file — pflag is now likely
	// already synced (from step 1), go-cmp is genuinely new. This is the
	// real SCOPE-003 set mechanism: one call covering a mix of
	// already-synced and new dependencies, not two separate requests
	// that could collide/coalesce imprecisely.
	t.Log("prioritize_file: syncing the real import set (pflag + go-cmp)")
	prioritizeText := callToolText(t, ctx, session, "prioritize_file", map[string]any{
		"project_id": projectID, "file": "main.go",
	})
	t.Logf("prioritize_file result: %s", truncateForLog(prioritizeText))
	if !strings.Contains(prioritizeText, "pflag") && !strings.Contains(prioritizeText, "spf13") {
		t.Errorf("prioritize_file result missing pflag: %s", prioritizeText)
	}
	if !strings.Contains(prioritizeText, "go-cmp") && !strings.Contains(prioritizeText, "cmp") {
		t.Errorf("prioritize_file result missing go-cmp: %s", prioritizeText)
	}

	// Step 3: search_dependency_docs against go-cmp — proves
	// prioritize_file's sync actually landed real, searchable content
	// for the dependency that was genuinely new, not just a bounded
	// still_building report.
	searchText := callToolText(t, ctx, session, "search_dependency_docs", map[string]any{
		"project_id": projectID, "query": "how do I compare two values", "dependency": "github.com/google/go-cmp",
	})
	if strings.Contains(searchText, "no synced knowledge") {
		t.Errorf("search_dependency_docs for go-cmp after prioritize_file = %q, want real content (prioritize_file's sync should have landed it)", searchText)
	} else {
		t.Logf("search_dependency_docs after prioritize_file: %s", truncateForLog(searchText))
	}
}

func truncateForLog(s string) string {
	const n = 300
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

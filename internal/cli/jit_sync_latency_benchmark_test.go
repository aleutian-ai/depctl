package cli

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/query"
)

// VALID-003: turns the one anecdotal JIT-sync timing number from this
// session's live testing (~6.3s for github.com/spf13/pflag, 304 chunks)
// into a repeatable, recorded measurement. Deliberately NOT part of the
// normal go test ./... run — it needs real network access, a real
// Ollama embedder, and a real Qdrant instance (this repo's own
// philosophy, established by VERIFY-001's non-goals, is that CI never
// depends on live external services) — set RAGCTL_LIVE_BENCHMARK=1 to
// run it manually. Skips (not fails) when that's unset, matching this
// repo's existing pattern for other live-network-dependent checks.
func TestJITSyncColdLatencyBenchmark(t *testing.T) {
	if os.Getenv("RAGCTL_LIVE_BENCHMARK") == "" {
		t.Skip("set RAGCTL_LIVE_BENCHMARK=1 to run this live benchmark (needs network, a running Ollama, and a running Qdrant on 127.0.0.1:6333)")
	}
	isolateEnv(t)
	requireGo(t)
	// requireGo defaults GOPROXY=off (this repo's other tests never need
	// real network resolution) — this benchmark is the one deliberate
	// exception, so it needs a real proxy to resolve a real module
	// version, not the fixtures' usual local-replace pattern.
	t.Setenv("GOPROXY", "https://proxy.golang.org,direct")
	runInitForTest(t)
	useRealRagctlBinary(t)

	// Point at the already-running local Qdrant/Ollama instead of
	// auto-bootstrapping a managed one — mirrors this session's own live
	// testing setup, and keeps this benchmark's timing about JIT-sync
	// itself, not container startup.
	cfgPath, err := config.DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Vector.Managed = false
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	type fixtureModule struct {
		name       string
		modulePath string // Go module path to depend on
	}
	modules := []fixtureModule{
		{"small (pflag)", "github.com/spf13/pflag"},
		{"medium (testify)", "github.com/stretchr/testify"},
	}

	for _, m := range modules {
		t.Run(m.name, func(t *testing.T) {
			root := t.TempDir()
			writeGoMod(t, root, "module example.com/benchmarkapp\n\ngo 1.21\n")
			// A real `go mod tidy` resolves a genuine current version —
			// `ragctl scan`'s own resolver only ever reads an already-
			// resolved go.mod/go.sum, it doesn't invent versions itself.
			tidyCmd := exec.Command("go", "get", m.modulePath+"@latest")
			tidyCmd.Dir = root
			tidyCmd.Env = os.Environ()
			if out, err := tidyCmd.CombinedOutput(); err != nil {
				t.Skipf("go get %s@latest failed (likely offline): %v\n%s", m.modulePath, err, out)
			}

			cmd := NewRootCmd()
			cmd.SetArgs([]string{"scan", root})
			if err := cmd.Execute(); err != nil {
				t.Skipf("scan failed (likely offline or module resolution issue): %v", err)
			}

			ctx := context.Background()
			c, err := ensureDaemon(ctx)
			if err != nil {
				t.Fatalf("ensureDaemon: %v", err)
			}
			svc := &daemonQueryService{c: c}
			status, err := svc.Status(ctx)
			if err != nil || len(status.Projects) == 0 {
				t.Fatalf("Status: %v, %+v", err, status)
			}
			projectID := status.Projects[len(status.Projects)-1].ID // most recently scanned

			trigger := &daemonSyncTrigger{c: c}
			syncStart := time.Now()
			synced, failed, skipped, err := trigger.SyncProject(ctx, projectID, m.modulePath, nil)
			syncElapsed := time.Since(syncStart)
			if err != nil {
				t.Fatalf("JIT SyncProject: %v", err)
			}
			if failed > 0 {
				t.Fatalf("JIT SyncProject: synced=%d failed=%d skipped=%d — want failed=0", synced, failed, skipped)
			}

			searchStart := time.Now()
			result, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: projectID, Dependency: m.modulePath, Text: "how do I use this package", Mode: query.ModeProject})
			searchElapsed := time.Since(searchStart)
			if err != nil {
				t.Fatalf("SearchKnowledge after JIT sync: %v", err)
			}

			t.Logf("%-20s sync=%-12s search=%-12s total=%-12s chunks=%d",
				m.modulePath, syncElapsed.Round(time.Millisecond), searchElapsed.Round(time.Millisecond),
				(syncElapsed + searchElapsed).Round(time.Millisecond), len(result.Chunks))
		})
	}
}

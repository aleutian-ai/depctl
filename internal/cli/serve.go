package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/daemon/client"
	"aleutian-ai/ragctl/internal/mcp"
	"aleutian-ai/ragctl/internal/symbolgraph"
	"aleutian-ai/ragctl/internal/symbolgraph/gopackages"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server over stdio",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd)
		},
	}
}

// runServe is a stdio↔daemon proxy (ADR-011 §8): it opens no store and
// builds no embedder itself. Every MCP tool call reaches query.Service,
// which runs inside the daemon, over the same socket every other
// store-touching command uses.
//
// server.mcp.enabled is checked twice, deliberately: once locally
// before touching the daemon at all (so a deliberately-disabled MCP
// server never pays the cost of auto-starting one), and again from the
// daemon's own /v1/health once connected, which is what actually gates
// EnableSyncTool too. The daemon owns config for its whole lifetime
// (see docs/internal/daemon.md) — its answer is authoritative if
// config.yaml was edited after it started, this process's own fresh
// read of it is not.
func runServe(cmd *cobra.Command) error {
	ctx := cmd.Context()

	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}
	if !cfg.Server.MCP.Enabled {
		return fmt.Errorf("MCP server disabled (server.mcp.enabled: false in config)")
	}

	c, err := ensureDaemon(ctx)
	if err != nil {
		return err
	}
	health, err := c.Health(ctx)
	if err != nil {
		return fmt.Errorf("check daemon health: %w", err)
	}
	if !health.MCPEnabled {
		return fmt.Errorf("MCP server disabled (server.mcp.enabled: false in config)")
	}

	deps := mcp.Deps{
		Query:          &daemonQueryService{c: c},
		Sync:           &daemonSyncTrigger{c: c},
		EnableSyncTool: health.EnableSyncTool,
		Scan:           &daemonScanTrigger{c: c},
		Priority:       &daemonPriorityBumper{c: c},
		Progress:       &daemonProgressReader{c: c},
		Symbols:        callSiteResolver(c),
	}
	startupScan(ctx, cmd.ErrOrStderr(), deps.Scan, deps.Query)

	server := mcp.New(deps)

	fmt.Fprintln(cmd.ErrOrStderr(), "ragctl MCP server starting (stdio transport)")
	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

// startupScan registers the server's own working directory (MCP-006)
// before the MCP loop starts, so an agent's first tool call already
// sees real dependency status instead of an empty fleet — the same
// scan_project path, just moved earlier; always safe to re-run. This is
// registration + status only (manifest parsing and version resolution),
// never a sync: no dependency is cloned or embedded here, so the cost
// stays bounded even for a session that never asks about dependencies.
// A failure here is logged, not fatal — the agent can always fall back
// to calling scan_project itself, mirroring callSiteResolver's own
// non-fatal posture above for a similar os.Getwd()-dependent step.
func startupScan(ctx context.Context, stderr io.Writer, scan mcp.ScanTrigger, q mcp.QueryService) {
	if scan == nil {
		return
	}
	ids, summary, err := scan.ScanProject(ctx, ".", nil)
	if err != nil {
		fmt.Fprintf(stderr, "ragctl: startup scan failed (%v) — call scan_project manually\n", err)
		return
	}
	if summary != "" {
		fmt.Fprint(stderr, summary)
	}
	if len(ids) == 0 || q == nil {
		return
	}
	status, err := q.Status(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ragctl: startup status check failed (%v)\n", err)
		return
	}
	fmt.Fprintf(stderr, "ragctl: %d project(s), %d dependencies, %d/%d already synced\n",
		status.TotalProjects, status.TotalDependencies, status.WithActiveGeneration, status.TotalDependencies)
}

// callSiteResolver builds explain_call_site's backing symbolgraph.Resolver
// (GRAPH-004), scoped to the MCP server's own working directory — the
// same "typically the project you're already in" convention scan_project
// already uses. Returns nil (explain_call_site reports "not configured")
// rather than failing serve's startup entirely if the working directory
// can't be determined; a real Go source tree failing to type-check is a
// per-call error, handled by the tool itself, not a startup failure.
func callSiteResolver(c *client.Client) mcp.CallSiteResolver {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return symbolgraph.New(gopackages.New(cwd), &daemonResolutionStore{c: c}, &daemonQueryService{c: c})
}

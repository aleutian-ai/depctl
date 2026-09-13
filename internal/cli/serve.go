package cli

import (
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/mcp"
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

	server := mcp.New(mcp.Deps{
		Query:          &daemonQueryService{c: c},
		Sync:           &daemonSyncTrigger{c: c},
		EnableSyncTool: health.EnableSyncTool,
		Scan:           &daemonScanTrigger{c: c},
	})

	fmt.Fprintln(cmd.ErrOrStderr(), "ragctl MCP server starting (stdio transport)")
	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

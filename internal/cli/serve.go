package cli

import (
	"context"
	"fmt"
	"io"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/mcp"
	"aleutian-ai/ragctl/internal/query"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the ragctl daemon (MCP/HTTP)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd)
		},
	}
}

func runServe(cmd *cobra.Command) error {
	ctx := context.Background()

	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}
	if !cfg.Server.MCP.Enabled {
		return fmt.Errorf("MCP server disabled (server.mcp.enabled: false in config)")
	}

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

	vb, err := buildVectorBackend(cfg)
	if err != nil {
		return err
	}
	embedder, err := buildEmbedder(cfg, badgerStore)
	if err != nil {
		return err
	}
	dims, err := embedder.Dimensions(ctx)
	if err != nil {
		return fmt.Errorf("probe embedder dimensions: %w", err)
	}
	ns := backend.Namespace{Name: cfg.Vector.Collection, Dimensions: dims, Distance: "cosine"}

	svc := query.New(store, badgerStore, vb, embedder, ns, cfg.Vector.Backend)
	sync := &syncTrigger{store: store, badgerStore: badgerStore, cfg: cfg}

	server := mcp.New(mcp.Deps{Query: svc, Sync: sync, EnableSyncTool: cfg.Server.MCP.EnableSyncTool})

	fmt.Fprintln(cmd.ErrOrStderr(), "ragctl MCP server starting (stdio transport)")
	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

// syncTrigger adapts RunSync into mcp.SyncTrigger, so the sync_project
// MCP tool executes the identical sync logic `ragctl sync` uses,
// against the same already-open store handles this long-running
// process holds.
type syncTrigger struct {
	store       *bboltstore.Store
	badgerStore *badgerstore.Store
	cfg         config.Config
}

func (t *syncTrigger) SyncProject(ctx context.Context, projectID string) (synced, failed, skipped int, err error) {
	return RunSync(ctx, t.store, t.badgerStore, t.cfg, projectID, "", false, false, io.Discard)
}

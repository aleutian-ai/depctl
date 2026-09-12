package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/project"
	"aleutian-ai/ragctl/internal/resolver"
	"aleutian-ai/ragctl/internal/resolver/golang"
	"aleutian-ai/ragctl/internal/resolver/node"
	"aleutian-ai/ragctl/internal/resolver/python"
)

// supportedEcosystems are the ecosystems ragctl currently ships a resolver
// for. It's a static set for now — it'll grow one entry at a time as later
// epics land.
var supportedEcosystems = map[domain.Ecosystem]bool{
	domain.EcosystemGo:     true,
	domain.EcosystemPython: true,
	domain.EcosystemNode:   true,
}

// resolvers maps ecosystem to the resolver.Resolver that resolves it. Only
// ecosystems in supportedEcosystems have an entry.
var resolvers = map[domain.Ecosystem]resolver.Resolver{
	domain.EcosystemGo:     golang.New(),
	domain.EcosystemPython: python.New(),
	domain.EcosystemNode:   node.New(),
}

func newScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan [path]",
		Short: "Scan a directory for projects",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runScan(cmd, root)
		},
	}
}

func runScan(cmd *cobra.Command, root string) error {
	// The daemon's working directory isn't the user's, so the path is
	// resolved here and sent absolute.
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", root, err)
	}
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	_, err = c.Resolve(cmd.Context(), abs, cmd.OutOrStdout())
	return err
}

// scanAndResolve discovers projects under root, registers them, and
// resolves each one's dependencies, returning the project IDs it
// touched. This is `ragctl scan`'s work, run inside the daemon.
func scanAndResolve(ctx context.Context, store *bboltstore.Store, root string, out io.Writer) ([]string, error) {
	detected, err := project.Scan(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}

	var ids []string
	var newCount, existingCount, unsupportedCount int

	for _, dp := range detected {
		if !supportedEcosystems[dp.Ecosystem] {
			unsupportedCount++
			fmt.Fprintf(out, "unsupported  %-8s %s\n", dp.Ecosystem, dp.Root)
			continue
		}

		id := project.ProjectID(dp.Root)
		existing, err := store.GetProject(ctx, id)
		isNew := errors.Is(err, bboltstore.ErrNotFound)
		if err != nil && !isNew {
			fmt.Fprintf(out, "error        %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
			continue
		}

		now := time.Now()
		p := domain.Project{ID: id, Root: dp.Root, CreatedAt: now, UpdatedAt: now}
		if !isNew {
			p.CreatedAt = existing.CreatedAt
		}
		if err := store.PutProject(ctx, p); err != nil {
			fmt.Fprintf(out, "error        %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
			continue
		}
		ids = append(ids, id)

		if isNew {
			newCount++
			fmt.Fprintf(out, "new          %-8s %s\n", dp.Ecosystem, dp.Root)
		} else {
			existingCount++
			fmt.Fprintf(out, "existing     %-8s %s\n", dp.Ecosystem, dp.Root)
		}

		res, err := resolvers[dp.Ecosystem].Resolve(ctx, dp.Root)
		if err != nil {
			fmt.Fprintf(out, "resolve error %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
			continue
		}
		if err := store.PutResolution(ctx, id, res); err != nil {
			fmt.Fprintf(out, "resolve error %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
			continue
		}
		fmt.Fprintf(out, "resolved     %-8s %s: %d dependencies\n", dp.Ecosystem, dp.Root, len(res.Dependencies))
	}

	fmt.Fprintf(out, "\ndiscovered %d, new %d, existing %d, unsupported %d\n",
		len(detected), newCount, existingCount, unsupportedCount)

	return ids, nil
}

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

// ErrUnregisteredProjectRoot is returned when something attempts to run
// a resolver command (`go list`, `cargo metadata`, `npm ls`, ...) against
// a directory that isn't a project ragctl has actually registered
// (SEC-004). In normal operation this is unreachable — scanAndResolve
// and resolveProject both already only ever call a Resolver with a root
// that was just persisted via PutProject, or read back from an existing
// domain.Project — this check makes that invariant explicit and enforced
// rather than merely true by the current call order, so a future
// refactor that reorders or bypasses registration fails loudly instead
// of quietly shelling out to a resolver command against an arbitrary,
// possibly attacker-influenced path (e.g. a fetched dependency's own
// worktree, which must never be treated as a project root).
var ErrUnregisteredProjectRoot = errors.New("refusing to run a resolver command: project root is not registered")

// requireRegisteredProjectRoot confirms id is a real, persisted project
// before a resolver is allowed to run against its root.
func requireRegisteredProjectRoot(ctx context.Context, store *bboltstore.Store, id string) error {
	if _, err := store.GetProject(ctx, id); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnregisteredProjectRoot, id, err)
	}
	return nil
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
//
// lockProject is held around each project's own persist step (registration
// plus resolve-and-store), never across the whole scan: two concurrent
// scans that discover different projects proceed independently, and only
// discovering the *same* project twice at once serializes — see
// daemon.Scheduler.LockProject, which is what a real caller passes.
func scanAndResolve(ctx context.Context, store *bboltstore.Store, root string, out io.Writer, lockProject func(string) func()) ([]string, error) {
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
		func() {
			unlock := lockProject(id)
			defer unlock()

			existing, err := store.GetProject(ctx, id)
			isNew := errors.Is(err, bboltstore.ErrNotFound)
			if err != nil && !isNew {
				fmt.Fprintf(out, "error        %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
				return
			}

			now := time.Now()
			p := domain.Project{ID: id, Root: dp.Root, CreatedAt: now, UpdatedAt: now}
			if !isNew {
				p.CreatedAt = existing.CreatedAt
			}
			if err := store.PutProject(ctx, p); err != nil {
				fmt.Fprintf(out, "error        %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
				return
			}
			ids = append(ids, id)

			if isNew {
				newCount++
				fmt.Fprintf(out, "new          %-8s %s\n", dp.Ecosystem, dp.Root)
			} else {
				existingCount++
				fmt.Fprintf(out, "existing     %-8s %s\n", dp.Ecosystem, dp.Root)
			}

			if err := requireRegisteredProjectRoot(ctx, store, id); err != nil {
				fmt.Fprintf(out, "resolve error %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
				return
			}
			res, err := resolvers[dp.Ecosystem].Resolve(ctx, dp.Root)
			if err != nil {
				fmt.Fprintf(out, "resolve error %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
				return
			}
			if err := store.PutResolution(ctx, id, res); err != nil {
				fmt.Fprintf(out, "resolve error %-8s %s: %v\n", dp.Ecosystem, dp.Root, err)
				return
			}
			fmt.Fprintf(out, "resolved     %-8s %s: %d dependencies\n", dp.Ecosystem, dp.Root, len(res.Dependencies))
		}()
	}

	fmt.Fprintf(out, "\ndiscovered %d, new %d, existing %d, unsupported %d\n",
		len(detected), newCount, existingCount, unsupportedCount)

	return ids, nil
}

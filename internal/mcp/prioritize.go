package mcp

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/query"
	"github.com/aleutian-ai/depctl/internal/symbolgraph"
)

// readinessPollInterval is how often ensureDependencies re-checks whether
// the dependencies it asked for are searchable yet. A var so tests can
// shorten it.
var readinessPollInterval = 500 * time.Millisecond

// jitDeps is what a tool needs to build dependencies on demand: the same
// pieces search_dependency_docs' just-in-time path uses (WATCH-019/020),
// bundled so several tools share one implementation.
type jitDeps struct {
	svc      QueryService
	sync     SyncTrigger
	priority PriorityBumper
	enabled  bool // server.mcp.enable_sync_tool
}

func (j jitDeps) available() bool { return j.enabled && j.sync != nil && j.svc != nil }

// ensureDependencies gets deps built next and waits, bounded by
// mcpSyncWaitBound, for them to become searchable. If a sync is already
// running for the project every dep is bumped to the front of its queue;
// otherwise one scoped sync for exactly this set is started, detached so
// it keeps running if the wait ends first. It returns the deps still not
// ready (empty on success) and an error only if the sync itself failed.
func ensureDependencies(ctx context.Context, j jitDeps, projectID string, deps []string) (pending []string, err error) {
	bumped := false
	if j.priority != nil {
		for _, dep := range deps {
			if ok, _ := j.priority.BumpSyncPriority(ctx, projectID, dep); ok {
				bumped = true
			}
		}
	}

	var syncDone chan error // stays nil when a running sync was bumped: nothing of ours to wait on
	if !bumped {
		syncDone = make(chan error, 1)
		bgCtx := context.WithoutCancel(ctx)
		go func() {
			_, failed, _, syncErr := j.sync.SyncProject(bgCtx, projectID, deps, false, nil)
			if syncErr == nil && failed > 0 {
				syncErr = fmt.Errorf("%d dependencies failed to sync", failed)
			}
			syncDone <- syncErr
		}()
	}

	deadline := time.Now().Add(mcpSyncWaitBound)
	for {
		pending = notReady(ctx, j.svc, projectID, deps)
		if len(pending) == 0 || time.Now().After(deadline) {
			return pending, nil
		}
		select {
		case syncErr := <-syncDone: // a nil channel never fires
			syncDone = nil
			if syncErr != nil {
				return notReady(ctx, j.svc, projectID, deps), syncErr
			}
		case <-ctx.Done():
			return pending, ctx.Err()
		case <-time.After(readinessPollInterval):
		}
	}
}

// notReady returns the names in want that don't have a searchable
// generation yet.
func notReady(ctx context.Context, svc QueryService, projectID string, want []string) []string {
	deps, err := svc.GetProjectDependencies(ctx, projectID)
	if err != nil {
		return want
	}
	ready := map[string]bool{}
	for _, d := range deps {
		if d.HasActiveGeneration {
			ready[d.Dependency.Dependency.Name] = true
		}
	}
	var pending []string
	for _, name := range want {
		if !ready[name] {
			pending = append(pending, name)
		}
	}
	return pending
}

// --- prioritize_file ---

// PrioritizeFileIn is prioritize_file's input.
type PrioritizeFileIn struct {
	ProjectID string `json:"project_id"`
	File      string `json:"file" jsonschema:"path of the Go file you are working on — absolute, or relative to the project root"`
}

// PrioritizedDependency is one of the file's imports that belongs to a
// resolved project dependency.
type PrioritizedDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
}

// PrioritizeFileOut is prioritize_file's result.
type PrioritizeFileOut struct {
	File          string                  `json:"file"`
	Imports       int                     `json:"imports"`
	Matched       []PrioritizedDependency `json:"matched,omitempty"`
	StillBuilding bool                    `json:"still_building,omitempty"`
	Note          string                  `json:"note"`
}

func prioritizeFileHandler(j jitDeps) sdkmcp.ToolHandlerFor[PrioritizeFileIn, PrioritizeFileOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in PrioritizeFileIn) (*sdkmcp.CallToolResult, PrioritizeFileOut, error) {
		if j.svc == nil {
			return nil, PrioritizeFileOut{}, errors.New("prioritize_file is not configured in this server")
		}
		root, err := projectRoot(ctx, j.svc, in.ProjectID)
		if err != nil {
			return nil, PrioritizeFileOut{}, toolError(err)
		}
		path := in.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		out := PrioritizeFileOut{File: path}

		if filepath.Ext(path) != ".go" {
			out.Note = "not a Go file — nothing to prioritize (only Go imports are understood so far)"
			return nil, out, nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return nil, PrioritizeFileOut{}, fmt.Errorf("prioritize_file: read %s: %w", path, err)
		}
		imports, ok := parseGoImports(source)
		out.Imports = len(imports)
		if !ok {
			out.Note = "the file could not be parsed as Go — nothing to prioritize"
			return nil, out, nil
		}

		projectDeps, err := j.svc.GetProjectDependencies(ctx, in.ProjectID)
		if err != nil {
			return nil, PrioritizeFileOut{}, toolError(err)
		}
		matched := matchImports(imports, projectDeps)
		if len(matched) == 0 {
			out.Note = "none of this file's imports belong to a resolved dependency of the project (standard library and the project's own packages are not dependencies) — nothing to prioritize"
			return nil, out, nil
		}

		var wanted []string
		for _, m := range matched {
			if !m.HasActiveGeneration {
				wanted = append(wanted, m.Dependency.Dependency.Name)
			}
		}
		if len(wanted) > 0 {
			if !j.available() {
				return nil, PrioritizeFileOut{}, errors.New("prioritize_file needs to build dependencies, but syncing is disabled by config (server.mcp.enable_sync_tool: false)")
			}
			pending, syncErr := ensureDependencies(ctx, j, in.ProjectID, wanted)
			if syncErr != nil {
				return nil, PrioritizeFileOut{}, toolError(syncErr)
			}
			out.StillBuilding = len(pending) > 0
		}

		still := map[string]bool{}
		if out.StillBuilding {
			for _, name := range notReady(ctx, j.svc, in.ProjectID, wanted) {
				still[name] = true
			}
		}
		for _, m := range matched {
			name := m.Dependency.Dependency.Name
			out.Matched = append(out.Matched, PrioritizedDependency{
				Name: name, Version: m.Dependency.Version,
				Ready: m.HasActiveGeneration || (len(wanted) > 0 && !still[name]),
			})
		}
		out.Note = prioritizeNote(out, len(wanted))
		return nil, out, nil
	}
}

func prioritizeNote(out PrioritizeFileOut, requested int) string {
	switch {
	case requested == 0:
		return fmt.Sprintf("all %d dependencies this file imports are already searchable", len(out.Matched))
	case out.StillBuilding:
		return fmt.Sprintf("%d of this file's %d dependencies were moved to the front of the build queue and are still building — search for one of them (it is built next regardless) or call sync_progress; this call did not fail", requested, len(out.Matched))
	default:
		return fmt.Sprintf("built %d dependencies this file imports; all %d are now searchable", requested, len(out.Matched))
	}
}

// projectRoot finds the registered root directory for projectID.
func projectRoot(ctx context.Context, svc QueryService, projectID string) (string, error) {
	status, err := svc.Status(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range status.Projects {
		if p.ID == projectID {
			return p.Root, nil
		}
	}
	return "", fmt.Errorf("%w: %s", query.ErrProjectNotFound, projectID)
}

// parseGoImports returns the import paths a Go source file declares. ok
// is false if the file doesn't parse; only the import block is read, so
// a body that doesn't compile doesn't matter.
func parseGoImports(source []byte) (imports []string, ok bool) {
	file, err := parser.ParseFile(token.NewFileSet(), "", source, parser.ImportsOnly)
	if err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		imports = append(imports, path)
	}
	return imports, true
}

// matchImports maps each import path to the Go dependency whose module
// path is its longest prefix (an import of cloud.google.com/go/billing/
// apiv1 belongs to module cloud.google.com/go/billing, not to
// cloud.google.com/go). An import with no owning dependency — the
// standard library, or the project's own packages — matches nothing.
// The result is unique per dependency, ordered by name.
func matchImports(imports []string, deps []query.ProjectDependency) []query.ProjectDependency {
	var goDeps []query.ProjectDependency
	for _, d := range deps {
		if d.Dependency.Dependency.Ecosystem == domain.EcosystemGo {
			goDeps = append(goDeps, d)
		}
	}
	matched := map[string]query.ProjectDependency{}
	for _, imp := range imports {
		best := -1
		for i, d := range goDeps {
			name := d.Dependency.Dependency.Name
			if (imp == name || strings.HasPrefix(imp, name+"/")) && (best < 0 || len(name) > len(goDeps[best].Dependency.Dependency.Name)) {
				best = i
			}
		}
		if best >= 0 {
			matched[goDeps[best].Dependency.Dependency.Name] = goDeps[best]
		}
	}
	out := make([]query.ProjectDependency, 0, len(matched))
	for _, d := range matched {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dependency.Dependency.Name < out[j].Dependency.Dependency.Name })
	return out
}

// justInTimeSyncedEvidence is explain_call_site's fallback (SCOPE-004):
// when a call site resolved to a dependency that hasn't been synced, build
// that one dependency next and retry once, mirroring what
// search_dependency_docs already does. still is true when the dependency
// isn't ready within the bound, which is not a failure.
func justInTimeSyncedEvidence(ctx context.Context, j jitDeps, symbols CallSiteResolver, projectID string, site symbolgraph.CallSite, queryText string, notSynced *symbolgraph.NotSyncedError) (bundle *symbolgraph.EvidenceBundle, still bool, err error) {
	pending, syncErr := ensureDependencies(ctx, j, projectID, []string{notSynced.Dependency})
	if syncErr != nil {
		return nil, false, notSynced // report the original, well-understood error
	}
	if len(pending) > 0 {
		return nil, true, nil
	}
	bundle, err = symbols.ResolveEvidence(ctx, projectID, site, queryText)
	return bundle, false, err
}

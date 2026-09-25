package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/query"
	"aleutian-ai/ragctl/internal/symbolgraph"
)

// registerTools wires every MCP-003 tool onto sdk, backed by deps.
func registerTools(sdk *sdkmcp.Server, deps Deps) {
	jit := jitDeps{svc: deps.Query, sync: deps.Sync, priority: deps.Priority, enabled: deps.EnableSyncTool}

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "search_dependency_docs",
		Description: "Search version-correct documentation/source for a project's dependency. Returns matched chunks with provenance. If the dependency hasn't been synced yet, this triggers a sync scoped to just that one dependency and retries automatically (when server.mcp.enable_sync_tool is true) — no need to call sync_project first for a single missing dependency.",
	}, searchDependencyDocsHandler(deps.Query, deps.Sync, deps.EnableSyncTool, deps.Priority))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "get_dependency_version",
		Description: "Get the exact resolved version of a package a project currently depends on.",
	}, getDependencyVersionHandler(deps.Query))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "list_project_dependencies",
		Description: "List every dependency a registered project resolves to, and whether each has synced knowledge available.",
	}, listProjectDependenciesHandler(deps.Query))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "get_release_changes",
		Description: "Get release-note excerpts for a dependency at two exact versions (not a full range walk — see tool output notes).",
	}, getReleaseChangesHandler(deps.Query))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "knowledge_status",
		Description: "Summarize how much of the registered fleet's dependencies have synced knowledge available, and list every registered project with its real project_id. Call this first if you don't already know the current project's project_id — every other tool requires the exact ID (e.g. \"proj_...\"), not a directory name or path.",
	}, knowledgeStatusHandler(deps.Query))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "sync_project",
		Description: "Trigger a knowledge sync for a project: builds/updates its searchable dependency documentation. Enabled by default (server.mcp.enable_sync_tool: false to disable for a read-only session). A first sync of a project with many dependencies clones and indexes each one, which can take a while — attach a progress token to the call to receive one notification per dependency as it completes. This call itself returns within a bounded time regardless: if the response has still_running: true, the sync did not fail and is continuing on the server — call sync_progress to see how far along it is, or call sync_project again, rather than treating it as an error.",
	}, syncProjectHandler(deps.Query, deps.Sync, deps.EnableSyncTool))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "prioritize_file",
		Description: "Tell ragctl which Go file you are about to work on, so the dependencies it imports are built next — ahead of the rest of the background sync — instead of waiting their turn. Pass the file (absolute, or relative to the project root). It reads the file's imports, matches them to the project's resolved dependencies, and requests exactly those, in one go. Returns within a bounded time: still_building: true means the rest of the file's dependencies are moving to the front and this did not fail — search for them or check sync_progress. A non-Go file, or one importing nothing from the project's dependencies, is a quiet no-op. Needs server.mcp.enable_sync_tool, like sync_project.",
	}, prioritizeFileHandler(jit))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "sync_progress",
		Description: "Check how far along a project's background sync is, without waiting on it: how many dependencies are done of the total, and which are being built right now with how many of their chunks are embedded. Read-only and instant — call it whenever sync_project returned still_running, or before deciding whether to wait. It is a progress report, not a time estimate: dependencies vary from seconds to many minutes. Anything you search for that isn't ready yet is built next, ahead of the rest of the queue.",
	}, syncProgressHandler(deps.Progress))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "scan_project",
		Description: "Discover and register the project(s) under a directory (default: the MCP server's own working directory, typically the project you're already in) so the other tools have a project_id to work with. Call this first whenever knowledge_status shows no matching project — it's always safe to (re-)run. Registration only; call sync_project afterward to actually build searchable knowledge.",
	}, scanProjectHandler(deps.Scan))

	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "explain_call_site",
		Description: "Resolve one source call site (file/line/column) to the exact-version dependency evidence relevant to it — what it's calling, and version-correct documentation for that call, without having to already know the dependency's name.",
	}, explainCallSiteHandler(deps.Symbols, jit))
}

// progressReporter returns nil (meaning "don't bother") when the call
// carried no progress token, so a client that never asks for progress
// costs nothing extra. Otherwise it returns a per-line callback that
// relays each line as an MCP progress notification (WATCH-013) with a
// strictly increasing Progress count against the given total (0 when
// unknown — the client renders that as indeterminate progress).
// NotifyProgress errors are ignored: a progress update is best-effort
// and must never fail the tool call itself.
func progressReporter(ctx context.Context, req *sdkmcp.CallToolRequest, total int) func(line string) {
	if req == nil || req.Params == nil {
		return nil
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil
	}
	done := 0
	return func(line string) {
		done++
		_ = req.Session.NotifyProgress(ctx, &sdkmcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      float64(done),
			Total:         float64(total),
			Message:       line,
		})
	}
}

// --- search_dependency_docs ---

type SearchDependencyDocsIn struct {
	ProjectID  string `json:"project_id" jsonschema:"the registered project ID (see list_project_dependencies or ragctl project list)"`
	Query      string `json:"query" jsonschema:"the natural-language search query"`
	Dependency string `json:"dependency,omitempty" jsonschema:"the exact package name to search within, e.g. google.golang.org/grpc"`
	Mode       string `json:"mode,omitempty" jsonschema:"one of project (default), latest, compare, all-retained"`
}

type SearchResultChunk struct {
	ChunkID    string            `json:"chunk_id"`
	Content    string            `json:"content"`
	Score      float32           `json:"score"`
	Ecosystem  string            `json:"ecosystem"`
	Dependency string            `json:"dependency"`
	Version    string            `json:"version"`
	Generation string            `json:"generation"`
	SourceType string            `json:"source_type"`
	Authority  int               `json:"authority"`
	TrustClass domain.TrustClass `json:"trust_class" jsonschema:"how much to trust this chunk: official/repository (high) vs community/user/unknown (lower) — weigh alongside authority when multiple chunks disagree"`
}

type SearchDependencyDocsOut struct {
	Chunks []SearchResultChunk `json:"chunks"`
	Note   string              `json:"note"`
}

// jitSyncPriorityWaitBound bounds how long searchDependencyDocsHandler
// waits for a priority-bumped dependency (WATCH-020) to become active
// within an already-running background sync — mirrors
// mcpSyncWaitBound's own calibration (WATCH-018) against real observed
// MCP client timeouts. A var, not a const, so tests can shorten it.
var jitSyncPriorityWaitBound = 90 * time.Second

// searchDependencyDocsHandler's sync/enableSync/priority params exist
// purely for WATCH-019/WATCH-020's JIT-sync-on-miss branch below — nil
// sync (or enableSync false) leaves search behavior identical to before
// those tickets; nil priority just means BumpSyncPriority is never
// tried, falling straight to WATCH-019's plain path.
func searchDependencyDocsHandler(svc QueryService, sync SyncTrigger, enableSync bool, priority PriorityBumper) sdkmcp.ToolHandlerFor[SearchDependencyDocsIn, SearchDependencyDocsOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in SearchDependencyDocsIn) (*sdkmcp.CallToolResult, SearchDependencyDocsOut, error) {
		mode := query.QueryMode(in.Mode)
		if mode == "" {
			mode = query.ModeProject
		}
		q := query.Query{ProjectID: in.ProjectID, Text: in.Query, Dependency: in.Dependency, Mode: mode}
		result, err := svc.SearchKnowledge(ctx, q)
		if err != nil && errors.Is(err, query.ErrNoActiveGeneration) && in.Dependency != "" && enableSync && sync != nil {
			// WATCH-020: try prioritizing within an already-running
			// background sync for this project first — a naive second
			// sync request would otherwise queue behind the whole
			// background run (the daemon serializes all sync work
			// globally, one at a time).
			bumped := false
			if priority != nil {
				bumped, _ = priority.BumpSyncPriority(ctx, in.ProjectID, in.Dependency)
			}
			if bumped {
				result, err = waitForDependencyGeneration(ctx, svc, q)
			} else {
				// WATCH-019: no background sync running — a resolvable
				// dependency simply hasn't been synced yet, so trigger
				// one scoped to just this package and retry once. If
				// the JIT sync itself fails, fall through and report the
				// original, well-understood error below rather than a
				// confusing second one from a call the agent didn't know
				// was happening.
				if _, failed, _, syncErr := sync.SyncProject(ctx, in.ProjectID, []string{in.Dependency}, nil); syncErr == nil && failed == 0 {
					result, err = svc.SearchKnowledge(ctx, q)
				}
			}
		}
		if err != nil {
			return nil, SearchDependencyDocsOut{}, toolError(err)
		}
		out := SearchDependencyDocsOut{Note: securityNote, Chunks: resultChunks(result.Chunks)}
		return nil, out, nil
	}
}

// resultChunks maps query.ResultChunk to the MCP wire type, shared by
// every tool that returns search results (search_dependency_docs,
// explain_call_site) so the field mapping lives in exactly one place.
func resultChunks(chunks []query.ResultChunk) []SearchResultChunk {
	out := make([]SearchResultChunk, len(chunks))
	for i, c := range chunks {
		out[i] = SearchResultChunk{
			ChunkID: c.ChunkID, Content: c.Content, Score: c.Score,
			Ecosystem: c.Ecosystem, Dependency: c.Dependency, Version: c.Version,
			Generation: c.Generation, SourceType: c.SourceType, Authority: c.Authority,
			TrustClass: c.TrustClass,
		}
	}
	return out
}

// --- get_dependency_version ---

type GetDependencyVersionIn struct {
	ProjectID string `json:"project_id"`
	Package   string `json:"package"`
}

type GetDependencyVersionOut struct {
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	Version   string `json:"version"`
	Note      string `json:"note"`
}

func getDependencyVersionHandler(svc QueryService) sdkmcp.ToolHandlerFor[GetDependencyVersionIn, GetDependencyVersionOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in GetDependencyVersionIn) (*sdkmcp.CallToolResult, GetDependencyVersionOut, error) {
		dep, err := svc.GetDependencyVersion(ctx, in.ProjectID, in.Package)
		if err != nil {
			return nil, GetDependencyVersionOut{}, toolError(err)
		}
		return nil, GetDependencyVersionOut{
			Ecosystem: string(dep.Dependency.Ecosystem), Package: dep.Dependency.Name, Version: dep.Version, Note: securityNote,
		}, nil
	}
}

// --- list_project_dependencies ---

type ListProjectDependenciesIn struct {
	ProjectID string `json:"project_id"`
}

type DependencyInfo struct {
	Ecosystem           string `json:"ecosystem"`
	Package             string `json:"package"`
	Version             string `json:"version"`
	Direct              bool   `json:"direct"`
	HasActiveGeneration bool   `json:"has_active_generation"`
}

type ListProjectDependenciesOut struct {
	Dependencies []DependencyInfo `json:"dependencies"`
	Note         string           `json:"note"`
}

func listProjectDependenciesHandler(svc QueryService) sdkmcp.ToolHandlerFor[ListProjectDependenciesIn, ListProjectDependenciesOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in ListProjectDependenciesIn) (*sdkmcp.CallToolResult, ListProjectDependenciesOut, error) {
		deps, err := svc.GetProjectDependencies(ctx, in.ProjectID)
		if err != nil {
			return nil, ListProjectDependenciesOut{}, toolError(err)
		}
		out := ListProjectDependenciesOut{Note: securityNote, Dependencies: make([]DependencyInfo, len(deps))}
		for i, d := range deps {
			out.Dependencies[i] = DependencyInfo{
				Ecosystem: string(d.Dependency.Dependency.Ecosystem), Package: d.Dependency.Dependency.Name,
				Version: d.Dependency.Version, Direct: d.Dependency.Dependency.Direct, HasActiveGeneration: d.HasActiveGeneration,
			}
		}
		return nil, out, nil
	}
}

// --- get_release_changes ---

type GetReleaseChangesIn struct {
	Dependency string `json:"dependency"`
	From       string `json:"from" jsonschema:"exact version string, e.g. v1.60.0"`
	To         string `json:"to" jsonschema:"exact version string, e.g. v1.67.0"`
}

type ReleaseChangeInfo struct {
	Ecosystem string `json:"ecosystem"`
	Version   string `json:"version"`
	Excerpt   string `json:"excerpt"`
}

type GetReleaseChangesOut struct {
	Changes []ReleaseChangeInfo `json:"changes"`
	Note    string              `json:"note"`
}

func getReleaseChangesHandler(svc QueryService) sdkmcp.ToolHandlerFor[GetReleaseChangesIn, GetReleaseChangesOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in GetReleaseChangesIn) (*sdkmcp.CallToolResult, GetReleaseChangesOut, error) {
		changes, err := svc.GetReleaseChanges(ctx, in.Dependency, in.From, in.To)
		if err != nil {
			return nil, GetReleaseChangesOut{}, toolError(err)
		}
		out := GetReleaseChangesOut{
			Note:    securityNote + "; only the exact from/to versions are returned, not every version in between",
			Changes: make([]ReleaseChangeInfo, len(changes)),
		}
		for i, c := range changes {
			out.Changes[i] = ReleaseChangeInfo{Ecosystem: c.Ecosystem, Version: c.Version, Excerpt: c.Excerpt}
		}
		return nil, out, nil
	}
}

// --- knowledge_status ---

type KnowledgeStatusIn struct{}

type ProjectRefOut struct {
	ProjectID string `json:"project_id"`
	Root      string `json:"root"`
}

type KnowledgeStatusOut struct {
	TotalProjects           int             `json:"total_projects"`
	TotalDependencies       int             `json:"total_dependencies"`
	WithActiveGeneration    int             `json:"with_active_generation"`
	WithoutActiveGeneration int             `json:"without_active_generation"`
	Projects                []ProjectRefOut `json:"projects"`
	Note                    string          `json:"note"`
}

func knowledgeStatusHandler(svc QueryService) sdkmcp.ToolHandlerFor[KnowledgeStatusIn, KnowledgeStatusOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in KnowledgeStatusIn) (*sdkmcp.CallToolResult, KnowledgeStatusOut, error) {
		status, err := svc.Status(ctx)
		if err != nil {
			return nil, KnowledgeStatusOut{}, toolError(err)
		}
		var projects []ProjectRefOut
		for _, p := range status.Projects {
			projects = append(projects, ProjectRefOut{ProjectID: p.ID, Root: p.Root})
		}
		return nil, KnowledgeStatusOut{
			TotalProjects: status.TotalProjects, TotalDependencies: status.TotalDependencies,
			WithActiveGeneration: status.WithActiveGeneration, WithoutActiveGeneration: status.WithoutActiveGeneration,
			Projects: projects,
			Note: securityNote + ". project_id here is the exact value every other tool's project_id argument requires — " +
				"resolve your project by matching \"root\" against the current working directory, not by guessing an ID from the directory name.",
		}, nil
	}
}

// --- sync_project ---

type SyncProjectIn struct {
	ProjectID  string `json:"project_id"`
	Dependency string `json:"dependency,omitempty" jsonschema:"optional — limit the sync to one package instead of the whole project"`
}

type SyncProjectOut struct {
	Synced        int    `json:"synced,omitempty"`
	Failed        int    `json:"failed,omitempty"`
	Skipped       int    `json:"skipped,omitempty"`
	StillRunning  bool   `json:"still_running,omitempty"`
	ReportedSoFar int    `json:"reported_so_far,omitempty"`
	Total         int    `json:"total,omitempty"`
	Note          string `json:"note"`
}

// mcpSyncWaitBound bounds how long syncProjectHandler waits for
// SyncTrigger.SyncProject before returning a partial, still-running
// response instead — so an MCP tool call never blocks a calling client
// past this, no matter how long the underlying sync legitimately takes
// (up to maxActionDuration, internal/daemon/scheduler.go).
//
// Live-found calibration: opencode's own tool-call timeout (unrelated
// to anything ragctl controls — the MCP spec doesn't standardize one)
// was observed at roughly 5 minutes in the session that found this gap.
// 90s sits well under that with real margin, while still being long
// enough that a typical sync of a handful of small-to-medium
// dependencies finishes within it and never hits the still-running path
// at all. A var, not a const, so tests can shorten it.
var mcpSyncWaitBound = 90 * time.Second

const syncStillRunningNote = "the sync is still running in the background and was not cancelled by this call returning — wait a bit and call sync_progress to see how far along it is (or list_project_dependencies/knowledge_status for what is already searchable), or call sync_project again (concurrent requests for the same project collapse into one, so this is never wasted work)"

func syncProjectHandler(query QueryService, sync SyncTrigger, enabled bool) sdkmcp.ToolHandlerFor[SyncProjectIn, SyncProjectOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in SyncProjectIn) (*sdkmcp.CallToolResult, SyncProjectOut, error) {
		if !enabled || sync == nil {
			return nil, SyncProjectOut{}, errors.New("sync_project is disabled by config (server.mcp.enable_sync_tool: false)")
		}
		total := 0
		if query != nil {
			if deps, err := query.GetProjectDependencies(ctx, in.ProjectID); err == nil {
				total = len(deps)
			}
		}

		var reported atomic.Int64
		notify := progressReporter(ctx, req, total)
		progress := func(line string) {
			reported.Add(1)
			if notify != nil {
				notify(line)
			}
		}

		type result struct {
			synced, failed, skipped int
			err                     error
		}
		done := make(chan result, 1)
		// Detached deliberately: if the select below times out and this
		// handler returns, the sync must keep running exactly as it
		// already does when a client disconnects mid-sync — see
		// scheduler.go's identical context.WithoutCancel(s.base) for the
		// same reasoning, one layer down.
		bgCtx := context.WithoutCancel(ctx)
		go func() {
			synced, failed, skipped, err := sync.SyncProject(bgCtx, in.ProjectID, dependencySet(in.Dependency), progress)
			done <- result{synced, failed, skipped, err}
		}()

		select {
		case r := <-done:
			if r.err != nil {
				return nil, SyncProjectOut{}, fmt.Errorf("sync_project: %w", r.err)
			}
			return nil, SyncProjectOut{Synced: r.synced, Failed: r.failed, Skipped: r.skipped, Note: securityNote}, nil
		case <-time.After(mcpSyncWaitBound):
			return nil, SyncProjectOut{
				StillRunning:  true,
				ReportedSoFar: int(reported.Load()),
				Total:         total,
				Note:          syncStillRunningNote,
			}, nil
		}
	}
}

// --- scan_project ---

type ScanProjectIn struct {
	Root string `json:"root,omitempty" jsonschema:"directory to scan for projects, absolute or relative to the MCP server's working directory; omit to scan that working directory itself (the common case — the directory the agent session is already operating in)"`
}

type ScanProjectOut struct {
	ProjectIDs []string `json:"project_ids"`
	Summary    string   `json:"summary"`
	Note       string   `json:"note"`
}

func scanProjectHandler(scan ScanTrigger) sdkmcp.ToolHandlerFor[ScanProjectIn, ScanProjectOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in ScanProjectIn) (*sdkmcp.CallToolResult, ScanProjectOut, error) {
		if scan == nil {
			return nil, ScanProjectOut{}, errors.New("scan_project is unavailable in this session")
		}
		root := in.Root
		if root == "" {
			root = "."
		}
		ids, summary, err := scan.ScanProject(ctx, root, progressReporter(ctx, req, 0))
		if err != nil {
			return nil, ScanProjectOut{}, fmt.Errorf("scan_project: %w", err)
		}
		return nil, ScanProjectOut{ProjectIDs: ids, Summary: summary, Note: securityNote}, nil
	}
}

// --- explain_call_site ---

type ExplainCallSiteIn struct {
	ProjectID string `json:"project_id" jsonschema:"the registered project ID"`
	File      string `json:"file" jsonschema:"path to the source file, relative to the project root"`
	Line      int    `json:"line" jsonschema:"1-indexed line number of the call site"`
	Column    int    `json:"column" jsonschema:"1-indexed column number of the call site"`
	Query     string `json:"query,omitempty" jsonschema:"optional — what to ask about the resolved symbol; defaults to the symbol's own qualified name if omitted"`
}

type ResolvedSymbol struct {
	Ecosystem     string `json:"ecosystem"`
	Module        string `json:"module"`
	Package       string `json:"package"`
	QualifiedName string `json:"qualified_name"`
	Version       string `json:"version"`
}

type ExplainCallSiteOut struct {
	Symbol *ResolvedSymbol     `json:"symbol,omitempty"`
	Chunks []SearchResultChunk `json:"chunks,omitempty"`
	// StillBuilding means the dependency this call site uses is being
	// built (SCOPE-004) but wasn't ready within the bounded wait — not a
	// failure; retry shortly.
	StillBuilding bool   `json:"still_building,omitempty"`
	Note          string `json:"note"`
}

func explainCallSiteHandler(symbols CallSiteResolver, jit jitDeps) sdkmcp.ToolHandlerFor[ExplainCallSiteIn, ExplainCallSiteOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in ExplainCallSiteIn) (*sdkmcp.CallToolResult, ExplainCallSiteOut, error) {
		if symbols == nil {
			return nil, ExplainCallSiteOut{}, errors.New("explain_call_site is not configured for this server")
		}
		site := symbolgraph.CallSite{File: in.File, Line: in.Line, Column: in.Column}

		bundle, err := symbols.ResolveEvidence(ctx, in.ProjectID, site, in.Query)
		var notSynced *symbolgraph.NotSyncedError
		if errors.As(err, &notSynced) && jit.available() {
			var still bool
			bundle, still, err = justInTimeSyncedEvidence(ctx, jit, symbols, in.ProjectID, site, in.Query, notSynced)
			if still {
				return nil, ExplainCallSiteOut{
					StillBuilding: true,
					Note:          "the dependency this call site uses (" + notSynced.Dependency + ") was moved to the front of the build queue but is still building — retry in a little while, or call sync_progress; this call did not fail",
				}, nil
			}
		}
		if err != nil {
			return nil, ExplainCallSiteOut{}, toolError(err)
		}
		if bundle == nil {
			return nil, ExplainCallSiteOut{Note: "this call site refers to code inside the project, not an external dependency"}, nil
		}

		return nil, ExplainCallSiteOut{
			Symbol: &ResolvedSymbol{
				Ecosystem: bundle.Symbol.Ecosystem, Module: bundle.Symbol.Module,
				Package: bundle.Symbol.Package, QualifiedName: bundle.Symbol.QualifiedName,
				Version: bundle.Dependency.Version,
			},
			Chunks: resultChunks(bundle.Result.Chunks),
			Note:   securityNote,
		}, nil
	}
}

// waitForDependencyGeneration polls SearchKnowledge until it stops
// reporting ErrNoActiveGeneration (the bumped dependency became active)
// or jitSyncPriorityWaitBound elapses — WATCH-020's counterpart to
// WATCH-018's bounded wait, for the case a background sync is already
// running rather than one this call started itself.
func waitForDependencyGeneration(ctx context.Context, svc QueryService, q query.Query) (query.SearchResult, error) {
	deadline := time.Now().Add(jitSyncPriorityWaitBound)
	for {
		result, err := svc.SearchKnowledge(ctx, q)
		if err == nil || !errors.Is(err, query.ErrNoActiveGeneration) || time.Now().After(deadline) {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// toolError maps query's (and, for explain_call_site, symbolgraph's)
// typed errors to actionable tool-facing messages (per MCP-003's
// failure-behavior requirement), falling back to the error's own
// message for anything else.
func toolError(err error) error {
	switch {
	case errors.Is(err, query.ErrProjectNotFound):
		return fmt.Errorf("project not registered — call the scan_project tool first: %w", err)
	case errors.Is(err, query.ErrDependencyNotFound):
		return fmt.Errorf("dependency not found for this project: %w", err)
	case errors.Is(err, query.ErrNoActiveGeneration):
		return fmt.Errorf("no synced knowledge for this version yet — run `ragctl sync`: %w", err)
	case errors.Is(err, symbolgraph.ErrDependencyNotResolved):
		return fmt.Errorf("this call site's dependency isn't in the project's resolved dependencies — call scan_project/sync_project, or the resolution may be stale: %w", err)
	default:
		return err
	}
}

// --- sync_progress ---

type SyncProgressIn struct {
	ProjectID string `json:"project_id"`
}

// InFlightDependencyOut is one dependency being built right now.
type InFlightDependencyOut struct {
	Name        string `json:"name"`
	ChunksDone  int    `json:"chunks_done"`
	ChunksTotal int    `json:"chunks_total"`
}

// ObservedTimingOut mirrors api.ObservedTiming — see its own doc
// comment for why median/p90, not a mean.
type ObservedTimingOut struct {
	Samples                 int     `json:"samples"`
	MedianDependencySeconds float64 `json:"median_dependency_seconds"`
	P90DependencySeconds    float64 `json:"p90_dependency_seconds"`
}

// SyncEstimateOut mirrors api.SyncEstimate — a rough projection only,
// weight it by Confidence, never treat RemainingSeconds as precise.
type SyncEstimateOut struct {
	RemainingSeconds float64 `json:"remaining_seconds"`
	Confidence       string  `json:"confidence"`
}

// SyncProgressOut is sync_progress's result. Done, Failed and Total count
// planned actions — nearly all one per dependency version.
type SyncProgressOut struct {
	Syncing  bool                    `json:"syncing"`
	Done     int                     `json:"done"`
	Failed   int                     `json:"failed"`
	Total    int                     `json:"total"`
	InFlight []InFlightDependencyOut `json:"in_flight,omitempty"`
	// BATCH-001 Option D: agent-facing scope-planning information — see
	// ObservedTimingOut/SyncEstimateOut. Use this, alongside Pending, to
	// decide whether to keep waiting, request a specific dependency via
	// sync_project(dependency: "..."), or accept partial coverage —
	// ragctl reports the data, it never makes that call itself.
	Observed *ObservedTimingOut `json:"observed,omitempty"`
	Estimate *SyncEstimateOut   `json:"estimate,omitempty"`
	Pending  []string           `json:"pending,omitempty"`
	Note     string             `json:"note"`
}

func syncProgressHandler(reader SyncProgressReader) sdkmcp.ToolHandlerFor[SyncProgressIn, SyncProgressOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in SyncProgressIn) (*sdkmcp.CallToolResult, SyncProgressOut, error) {
		if reader == nil {
			return nil, SyncProgressOut{}, errors.New("sync_progress is not configured in this server")
		}
		out, err := reader.SyncProgress(ctx, in.ProjectID)
		if err != nil {
			return nil, SyncProgressOut{}, toolError(err)
		}
		out.Note = syncProgressNote(out)
		return nil, out, nil
	}
}

func syncProgressNote(p SyncProgressOut) string {
	switch {
	case p.Syncing:
		note := fmt.Sprintf("a sync is running: %d of %d done (%d failed).", p.Done, p.Total, p.Failed)
		if p.Estimate != nil {
			note += fmt.Sprintf(" Observed so far: median %.0fs/dependency (p90 %.0fs, %d samples); rough estimate ~%.0fs remaining, confidence %s — not precise, one large dependency can still take much longer than the median.", p.Observed.MedianDependencySeconds, p.Observed.P90DependencySeconds, p.Observed.Samples, p.Estimate.RemainingSeconds, p.Estimate.Confidence)
		} else {
			note += " This is progress, not a time estimate yet — too few dependencies have finished to project one."
		}
		note += " A dependency you search for that isn't done yet is built next, ahead of the rest of the queue. If you only need specific dependencies, call sync_project(dependency: \"...\") for each rather than waiting on the whole batch — see pending for what's left."
		return note
	case p.Total > 0:
		return fmt.Sprintf("no sync is running; the last run finished %d of %d (%d failed). Call sync_project to run another.", p.Done, p.Total, p.Failed)
	default:
		return "no sync has run for this project yet. Call sync_project to start one."
	}
}

// dependencySet turns a tool's optional single dependency argument into
// the sync scope: empty means the whole project.
func dependencySet(dependency string) []string {
	if dependency == "" {
		return nil
	}
	return []string{dependency}
}

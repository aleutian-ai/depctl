package mcp

import (
	"context"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/query"
)

// registerTools wires every MCP-003 tool onto sdk, backed by deps.
func registerTools(sdk *sdkmcp.Server, deps Deps) {
	sdkmcp.AddTool(sdk, &sdkmcp.Tool{
		Name:        "search_dependency_docs",
		Description: "Search version-correct documentation/source for a project's dependency. Returns matched chunks with provenance.",
	}, searchDependencyDocsHandler(deps.Query))

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
		Description: "Trigger a knowledge sync for a project. Disabled by default (server.mcp.enable_sync_tool: false) since a read-only client should never trigger network activity/writes unintentionally.",
	}, syncProjectHandler(deps.Sync, deps.EnableSyncTool))
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

func searchDependencyDocsHandler(svc QueryService) sdkmcp.ToolHandlerFor[SearchDependencyDocsIn, SearchDependencyDocsOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in SearchDependencyDocsIn) (*sdkmcp.CallToolResult, SearchDependencyDocsOut, error) {
		mode := query.QueryMode(in.Mode)
		if mode == "" {
			mode = query.ModeProject
		}
		result, err := svc.SearchKnowledge(ctx, query.Query{
			ProjectID: in.ProjectID, Text: in.Query, Dependency: in.Dependency, Mode: mode,
		})
		if err != nil {
			return nil, SearchDependencyDocsOut{}, toolError(err)
		}
		out := SearchDependencyDocsOut{Note: securityNote, Chunks: make([]SearchResultChunk, len(result.Chunks))}
		for i, c := range result.Chunks {
			out.Chunks[i] = SearchResultChunk{
				ChunkID: c.ChunkID, Content: c.Content, Score: c.Score,
				Ecosystem: c.Ecosystem, Dependency: c.Dependency, Version: c.Version,
				Generation: c.Generation, SourceType: c.SourceType, Authority: c.Authority,
				TrustClass: c.TrustClass,
			}
		}
		return nil, out, nil
	}
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
	ProjectID string `json:"project_id"`
}

type SyncProjectOut struct {
	Synced  int    `json:"synced"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
	Note    string `json:"note"`
}

func syncProjectHandler(sync SyncTrigger, enabled bool) sdkmcp.ToolHandlerFor[SyncProjectIn, SyncProjectOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in SyncProjectIn) (*sdkmcp.CallToolResult, SyncProjectOut, error) {
		if !enabled || sync == nil {
			return nil, SyncProjectOut{}, errors.New("sync_project is disabled by config (server.mcp.enable_sync_tool: false)")
		}
		synced, failed, skipped, err := sync.SyncProject(ctx, in.ProjectID)
		if err != nil {
			return nil, SyncProjectOut{}, fmt.Errorf("sync_project: %w", err)
		}
		return nil, SyncProjectOut{Synced: synced, Failed: failed, Skipped: skipped, Note: securityNote}, nil
	}
}

// toolError maps query's typed errors to actionable tool-facing
// messages (per MCP-003's failure-behavior requirement), falling back
// to the error's own message for anything else.
func toolError(err error) error {
	switch {
	case errors.Is(err, query.ErrProjectNotFound):
		return fmt.Errorf("project not registered — run `ragctl scan` first: %w", err)
	case errors.Is(err, query.ErrDependencyNotFound):
		return fmt.Errorf("dependency not found for this project: %w", err)
	case errors.Is(err, query.ErrNoActiveGeneration):
		return fmt.Errorf("no synced knowledge for this version yet — run `ragctl sync`: %w", err)
	default:
		return err
	}
}

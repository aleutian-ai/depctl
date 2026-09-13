package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"aleutian-ai/ragctl/internal/daemon/api"
	"aleutian-ai/ragctl/internal/daemon/client"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/query"
)

// daemonQueryService implements mcp.QueryService over the daemon's HTTP
// API — the client-side half of ADR-011 §8: `ragctl serve` reaches
// query.Service, which runs inside the daemon, over the socket instead
// of opening a store and building its own embedder/vector backend.
type daemonQueryService struct {
	c *client.Client
}

// Status summarizes fleet-wide sync coverage, the work behind the
// knowledge_status MCP tool.
func (q *daemonQueryService) Status(ctx context.Context) (query.Status, error) {
	resp, err := q.c.KnowledgeStatus(ctx)
	if err != nil {
		return query.Status{}, err
	}
	projects := make([]query.ProjectRef, len(resp.Projects))
	for i, p := range resp.Projects {
		projects[i] = query.ProjectRef{ID: p.ID, Root: p.Root}
	}
	return query.Status{
		TotalProjects:           resp.TotalProjects,
		TotalDependencies:       resp.TotalDependencies,
		WithActiveGeneration:    resp.WithActiveGeneration,
		WithoutActiveGeneration: resp.WithoutActiveGeneration,
		Projects:                projects,
	}, nil
}

// GetProjectDependencies lists a project's resolved dependencies, the
// work behind the list_project_dependencies MCP tool.
func (q *daemonQueryService) GetProjectDependencies(ctx context.Context, projectID string) ([]query.ProjectDependency, error) {
	resp, err := q.c.ProjectDependencies(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]query.ProjectDependency, len(resp.Dependencies))
	for i, d := range resp.Dependencies {
		out[i] = query.ProjectDependency{
			Dependency: domain.DependencyVersion{
				Dependency: domain.Dependency{Ecosystem: domain.Ecosystem(d.Ecosystem), Name: d.Name, Direct: d.Direct},
				Version:    d.Version,
				ResolvedBy: d.ResolvedBy,
			},
			HasActiveGeneration: d.HasActiveGeneration,
		}
	}
	return out, nil
}

// GetDependencyVersion resolves one package's version within a project,
// the work behind the get_dependency_version MCP tool.
func (q *daemonQueryService) GetDependencyVersion(ctx context.Context, projectID, pkg string) (domain.DependencyVersion, error) {
	resp, err := q.c.DependencyVersion(ctx, projectID, pkg)
	if err != nil {
		return domain.DependencyVersion{}, err
	}
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.Ecosystem(resp.Ecosystem), Name: resp.Name, Direct: resp.Direct},
		Version:    resp.Version,
		ResolvedBy: resp.ResolvedBy,
		Checksum:   resp.Checksum,
	}, nil
}

// GetReleaseChanges gets release-note excerpts between two versions of a
// dependency, the work behind the get_release_changes MCP tool.
func (q *daemonQueryService) GetReleaseChanges(ctx context.Context, dependency, from, to string) ([]query.ReleaseChange, error) {
	resp, err := q.c.ReleaseChanges(ctx, dependency, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]query.ReleaseChange, len(resp.Changes))
	for i, c := range resp.Changes {
		out[i] = query.ReleaseChange{Ecosystem: c.Ecosystem, Version: c.Version, Excerpt: c.Excerpt}
	}
	return out, nil
}

// SearchKnowledge runs a knowledge search, the work behind the
// search_dependency_docs MCP tool.
func (q *daemonQueryService) SearchKnowledge(ctx context.Context, req query.Query) (query.SearchResult, error) {
	resp, err := q.c.Search(ctx, api.SearchRequest{
		ProjectID:  req.ProjectID,
		Text:       req.Text,
		Dependency: req.Dependency,
		Mode:       string(req.Mode),
		TopK:       req.TopK,
	})
	if err != nil {
		return query.SearchResult{}, err
	}
	chunks := make([]query.ResultChunk, len(resp.Chunks))
	for i, c := range resp.Chunks {
		chunks[i] = query.ResultChunk{
			ChunkID:    c.ChunkID,
			Content:    c.Content,
			Score:      c.Score,
			Ecosystem:  c.Ecosystem,
			Dependency: c.Dependency,
			Version:    c.Version,
			Generation: c.Generation,
			SourceType: c.SourceType,
			Authority:  c.Authority,
			TrustClass: domain.TrustClass(c.TrustClass),
		}
	}
	return query.SearchResult{Chunks: chunks}, nil
}

// daemonSyncTrigger implements mcp.SyncTrigger over the daemon's HTTP
// API, so the sync_project MCP tool goes through the same
// Scheduler.Request every other sync trigger (CLI, watch) already does,
// instead of calling RunSync directly against a store serve opened
// itself.
type daemonSyncTrigger struct {
	c *client.Client
}

func (t *daemonSyncTrigger) SyncProject(ctx context.Context, projectID string, progress func(line string)) (synced, failed, skipped int, err error) {
	resp, err := t.c.Sync(ctx, api.SyncRequest{ProjectID: projectID}, lineWriter(progress))
	if err != nil {
		return 0, 0, 0, err
	}
	for _, r := range resp.Results {
		synced += r.Synced
		failed += r.Failed
		skipped += r.Skipped
	}
	return synced, failed, skipped, nil
}

// daemonScanTrigger implements mcp.ScanTrigger over the daemon's HTTP
// API, so the scan_project MCP tool goes through the same
// Scheduler.LockProject-guarded resolve every other scan trigger (CLI)
// already does.
type daemonScanTrigger struct {
	c *client.Client
}

func (t *daemonScanTrigger) ScanProject(ctx context.Context, root string, progress func(line string)) (ids []string, summary string, err error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s: %w", root, err)
	}
	var buf bytes.Buffer
	res, err := t.c.Resolve(ctx, abs, io.MultiWriter(&buf, lineWriter(progress)))
	if err != nil {
		return nil, buf.String(), err
	}
	return res.ProjectIDs, buf.String(), nil
}

// lineWriter adapts a per-line callback to io.Writer, for handing to
// client.Client's streamed calls (WATCH-013). client.stream already
// delivers one already-newline-terminated server line per Write call
// (fmt.Fprintln(out, line.Log)), so the buffering here is a defensive
// fallback, not load-bearing. fn may be nil, meaning no callback is
// wanted — that's the plain sync/scan case, no progress token attached.
func lineWriter(fn func(line string)) io.Writer {
	if fn == nil {
		return io.Discard
	}
	return &lineCallbackWriter{fn: fn}
}

type lineCallbackWriter struct {
	fn  func(line string)
	buf bytes.Buffer
}

func (w *lineCallbackWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		chunk, err := w.buf.ReadBytes('\n')
		if err != nil {
			w.buf.Write(chunk) // incomplete line: put the unread remainder back
			break
		}
		w.fn(strings.TrimRight(string(chunk), "\n"))
	}
	return len(p), nil
}

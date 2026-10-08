package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/aleutian-ai/depctl/internal/daemon/api"
)

// handleResolve runs `depctl scan`'s discover-and-resolve for one root,
// then refreshes the watched set so a newly registered project is
// watched without waiting for the periodic refresh.
//
// Bounded by maxActionDuration, the same ceiling GC runs use: scan
// doesn't go through the scheduler (it's not mutually exclusive with
// anything but a scan of the same project, via Scheduler.LockProject),
// so without its own bound a hung resolver call had no ceiling at all.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req api.ResolveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Root == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("root must not be empty"))
		return
	}

	// r.Context() already carries the daemon's real logger — see
	// loggingMiddleware, applied to every route in routes().
	ctx, cancel := context.WithTimeout(r.Context(), maxActionDuration)
	defer cancel()

	stream(w, func(out io.Writer) (any, error) {
		ids, err := s.opts.Engine.Scan(ctx, req.Root, out, s.scheduler.LockProject)
		s.refreshProjects(ctx)
		if err != nil {
			return nil, err
		}
		return api.ResolveResult{ProjectIDs: ids}, nil
	})
}

// handleSyncPriority asks the currently-running sync for the requested
// project, if any, to prioritize the requested dependency next
// (WATCH-020) — read-only from the scheduler's perspective otherwise,
// so it never blocks: a plain map lookup plus an append, not a stream.
func (s *Server) handleSyncPriority(w http.ResponseWriter, r *http.Request) {
	var req api.SyncPriorityRequest
	if !decodeBody(w, r, &req) {
		return
	}
	bumped := s.scheduler.BumpPriority(req.ProjectID, req.Dependency)
	writeJSON(w, http.StatusOK, api.SyncPriorityResponse{Bumped: bumped})
}

// handleSyncProgress reports a project's sync progress (SCOPE-001): a
// cheap read of the scheduler's counters, so it never blocks or starts
// anything.
func (s *Server) handleSyncProgress(w http.ResponseWriter, r *http.Request) {
	var req api.SyncProgressRequest
	if !decodeBody(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusOK, s.scheduler.SyncProgress(req.ProjectID))
}

// handleSync queues sync work through the scheduler and waits for the
// run that covers it, so a CLI sync and a watch-triggered sync can never
// run at the same time for one project.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var req api.SyncRequest
	if !decodeBody(w, r, &req) {
		return
	}

	stream(w, func(out io.Writer) (any, error) {
		ids := []string{req.ProjectID}
		if req.ProjectID == "" {
			var err error
			if ids, err = s.opts.Engine.ProjectIDs(r.Context()); err != nil {
				return nil, err
			}
		}
		opts := SyncOptions{Dependencies: req.DependencySet(), Offline: req.Offline, Force: req.Force, Rebuild: req.Rebuild}

		resp := api.SyncResponse{}
		for _, id := range ids {
			if s.busy(id) {
				fmt.Fprintf(out, "sync already running for %s; queued a follow-up\n", id)
			}
			result := <-s.scheduler.Request(id, opts, out)
			if result.Err != nil {
				return nil, result.Err
			}
			result.Sync.ProjectID = id
			resp.Results = append(resp.Results, result.Sync)
		}
		s.refreshProjects(r.Context())
		return resp, nil
	})
}

// handlePlan returns the desired-state plan as the CLI's own JSON shape;
// it is read-only and never queues work.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req api.PlanRequest
	if !decodeBody(w, r, &req) {
		return
	}
	plans, err := s.opts.Engine.Plan(r.Context(), req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

// handleSearch, handleProjectDependencies, handleDependencyVersion,
// handleReleaseChanges, and handleKnowledgeStatus back the MCP query
// tools (internal/mcp.QueryService). All read-only, like handlePlan —
// none queue work through the scheduler.

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req api.SearchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp, err := s.opts.Engine.Search(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProjectDependencies(w http.ResponseWriter, r *http.Request) {
	var req api.ProjectDependenciesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp, err := s.opts.Engine.ProjectDependencies(r.Context(), req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDependencyVersion(w http.ResponseWriter, r *http.Request) {
	var req api.DependencyVersionRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp, err := s.opts.Engine.DependencyVersion(r.Context(), req.ProjectID, req.Package)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReleaseChanges(w http.ResponseWriter, r *http.Request) {
	var req api.ReleaseChangesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp, err := s.opts.Engine.ReleaseChanges(r.Context(), req.Dependency, req.From, req.To)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	resp, err := s.opts.Engine.KnowledgeStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProjectList(w http.ResponseWriter, r *http.Request) {
	resp, err := s.opts.Engine.ProjectList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	var req api.ProjectGetRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp, err := s.opts.Engine.ProjectGet(r.Context(), req.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDescribe(w http.ResponseWriter, r *http.Request) {
	var req api.DescribeRequest
	if !decodeBody(w, r, &req) {
		return
	}
	report, err := s.opts.Engine.Describe(r.Context(), req.Args, req.CheckLiveness)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	resp, err := s.opts.Engine.Doctor(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGC runs garbage collection through the scheduler, exactly like
// handleSync: it excludes every in-flight build and reference change
// (BuildCoordinator.ExcludeForGC, so GC never interleaves with a sync), is
// bounded by maxActionDuration so a hung GC can't stall every build, and a
// GC request that arrives while one is already running collapses into a
// single follow-up.
// req.Orphans selects GC-001/GC-002's separate orphan-generation path
// instead (RequestOrphanGC), and req.SupersededDuplicates selects
// POINT-004's same-version-duplicate path the same way — no
// request-coalescing on either, since both are deliberately manual/opt-in,
// never fired automatically the way sync (and therefore reference-based
// GC's own coalescing need) is.
func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	var req api.GCRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Orphans {
		stream(w, func(out io.Writer) (any, error) {
			return s.scheduler.RequestOrphanGC(r.Context(), s.opts.Engine.OrphanGC, req.DryRun, out)
		})
		return
	}
	if req.SupersededDuplicates {
		stream(w, func(out io.Writer) (any, error) {
			return s.scheduler.RequestOrphanGC(r.Context(), s.opts.Engine.SupersededDuplicatesGC, req.DryRun, out)
		})
		return
	}
	stream(w, func(out io.Writer) (any, error) {
		if s.gcBusy() {
			fmt.Fprintf(out, "gc already running; queued a follow-up\n")
		}
		outcome := <-s.scheduler.RequestGC(req.DryRun, out)
		if outcome.Err != nil {
			return nil, outcome.Err
		}
		return outcome.GC, nil
	})
}

// handleExportMem0 runs MEM0-001's export. Read-only against the
// stores, but potentially long-running (one outbound HTTP push per
// chunk against the user's own Mem0 instance), so it streams progress
// like handleSync/handleResolve rather than blocking for one response.
// Bounded by maxActionDuration, the same ceiling every other
// non-scheduler-routed long-running route uses.
func (s *Server) handleExportMem0(w http.ResponseWriter, r *http.Request) {
	var req api.ExportMem0Request
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("project_id must not be empty"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxActionDuration)
	defer cancel()

	stream(w, func(out io.Writer) (any, error) {
		return s.opts.Engine.ExportMem0(ctx, req, out)
	})
}

// handleExportGraphiti runs GRAPHITI-001's export — same shape as
// handleExportMem0.
func (s *Server) handleExportGraphiti(w http.ResponseWriter, r *http.Request) {
	var req api.ExportGraphitiRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("project_id must not be empty"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxActionDuration)
	defer cancel()
	stream(w, func(out io.Writer) (any, error) {
		return s.opts.Engine.ExportGraphiti(ctx, req, out)
	})
}

// handleExportCognee runs COGNEE-001's export — same shape as
// handleExportMem0.
func (s *Server) handleExportCognee(w http.ResponseWriter, r *http.Request) {
	var req api.ExportCogneeRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("project_id must not be empty"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxActionDuration)
	defer cancel()
	stream(w, func(out io.Writer) (any, error) {
		return s.opts.Engine.ExportCognee(ctx, req, out)
	})
}

// gcBusy reports whether GC is currently running, so the client can be
// told its request was folded into a follow-up run.
func (s *Server) gcBusy() bool {
	s.scheduler.mu.Lock()
	defer s.scheduler.mu.Unlock()
	return s.scheduler.gc.running
}

// syncActivity names every in-flight sync for `depctl status`. A project
// the engine can't name (removed mid-sync) is shown by ID alone.
func (s *Server) syncActivity(ctx context.Context) []api.ProjectSync {
	running := s.scheduler.SyncingProjects()
	if len(running) == 0 {
		return nil
	}
	roots := map[string]string{}
	if projects, err := s.opts.Engine.Projects(ctx); err == nil {
		for _, p := range projects {
			roots[p.ID] = p.Root
		}
	}
	syncs := make([]api.ProjectSync, 0, len(running))
	for id, progress := range running {
		syncs = append(syncs, api.ProjectSync{ProjectID: id, Root: roots[id], SyncProgress: progress})
	}
	sort.Slice(syncs, func(i, j int) bool { return syncs[i].ProjectID < syncs[j].ProjectID })
	return syncs
}

// busy reports whether a project is mid-sync, so the client can be told
// its request was folded into a follow-up run.
func (s *Server) busy(projectID string) bool {
	return s.scheduler.States()[projectID] != "idle"
}

// decodeBody reads a JSON request body, answering the client itself if
// it is malformed. An empty body is allowed and leaves v zeroed.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read request body: %w", err))
		return false
	}
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request body: %w", err))
		return false
	}
	return true
}

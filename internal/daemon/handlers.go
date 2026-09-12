package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// handleResolve runs `ragctl scan`'s discover-and-resolve for one root,
// then refreshes the watched set so a newly registered project is
// watched without waiting for the periodic refresh.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req api.ResolveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Root == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("root must not be empty"))
		return
	}

	stream(w, func(out io.Writer) (any, error) {
		ids, err := s.opts.Engine.Scan(r.Context(), req.Root, out, s.scheduler.LockProject)
		s.refreshProjects(r.Context())
		if err != nil {
			return nil, err
		}
		return api.ResolveResult{ProjectIDs: ids}, nil
	})
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
		opts := SyncOptions{Dependency: req.Dependency, Offline: req.Offline, Force: req.Force}

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

// handleGC runs garbage collection through the scheduler, exactly like
// handleSync: it goes through the same global run lock as every sync (so
// GC never interleaves with one), is bounded by maxActionDuration so a
// hung GC can't wedge every future sync, and a GC request that arrives
// while one is already running collapses into a single follow-up.
func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	var req api.GCRequest
	if !decodeBody(w, r, &req) {
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

// gcBusy reports whether GC is currently running, so the client can be
// told its request was folded into a follow-up run.
func (s *Server) gcBusy() bool {
	s.scheduler.mu.Lock()
	defer s.scheduler.mu.Unlock()
	return s.scheduler.gc.running
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

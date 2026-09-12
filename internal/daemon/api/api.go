// Package api defines the wire contract of the local ragctl daemon API:
// the route paths plus the JSON request and response types shared by the
// server (internal/daemon) and its clients (internal/daemon/client), so
// the two can't drift. Nothing here talks to storage or the network.
package api

import (
	"encoding/json"
	"time"
)

// Route paths. Every route is versioned so an older client fails on an
// unknown path rather than misreading a changed body.
const (
	PathHealth   = "/v1/health"
	PathStatus   = "/v1/status"
	PathShutdown = "/v1/shutdown"
	PathResolve  = "/v1/projects/resolve"
	PathPlan     = "/v1/plan"
	PathSync     = "/v1/sync"
	PathGC       = "/v1/gc"
)

// Health is what GET /v1/health reports: enough to identify the running
// daemon and the config it loaded, without touching the stores.
type Health struct {
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	Socket         string    `json:"socket"`
	ControlPath    string    `json:"control_path"`
	Version        string    `json:"version"`
	Watching       bool      `json:"watching"`
	MCPEnabled     bool      `json:"mcp_enabled"`
	EnableSyncTool bool      `json:"enable_sync_tool"`
}

// Status is the `ragctl status` snapshot; text and --json both render
// this one struct.
type Status struct {
	Projects             int           `json:"projects"`
	DependencyReferences int           `json:"dependency_references"`
	ActiveGenerations    int           `json:"active_generations"`
	Jobs                 JobStats      `json:"jobs"`
	StorageBboltBytes    int64         `json:"storage_bbolt_bytes"`
	StorageBadgerBytes   int64         `json:"storage_badger_bytes"`
	Backend              BackendStatus `json:"backend"`
	LastSync             *time.Time    `json:"last_sync"`
	GCRunning            bool          `json:"gc_running"`
}

// JobStats counts jobs by state class. RETRY counts as pending: it is
// waiting to run again, not running.
type JobStats struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	Failed  int `json:"failed"`
}

// BackendStatus names the configured vector backend and whether its
// health probe passed.
type BackendStatus struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
}

// SyncResult is the outcome of one sync run, matching what `ragctl
// sync` summarizes.
type SyncResult struct {
	ProjectID string `json:"project_id"`
	Synced    int    `json:"synced"`
	Failed    int    `json:"failed"`
	Skipped   int    `json:"skipped"`
}

// ResolveRequest asks the daemon to scan and resolve a directory, the
// work behind `ragctl scan`. Root must be absolute: the daemon's working
// directory is not the caller's.
type ResolveRequest struct {
	Root string `json:"root"`
}

// ResolveResult reports which projects a resolve touched.
type ResolveResult struct {
	ProjectIDs []string `json:"project_ids"`
}

// SyncRequest is one `ragctl sync` invocation. An empty ProjectID means
// every registered project, as the CLI's missing --project does.
type SyncRequest struct {
	ProjectID  string `json:"project_id,omitempty"`
	Dependency string `json:"dependency,omitempty"`
	Offline    bool   `json:"offline,omitempty"`
	Force      bool   `json:"force,omitempty"`
}

// SyncResponse carries one result per project the request covered.
type SyncResponse struct {
	Results []SyncResult `json:"results"`
}

// PlanRequest asks for the desired-state plan without changing anything.
type PlanRequest struct {
	ProjectID string `json:"project_id,omitempty"`
}

// GCRequest is one `ragctl gc` invocation.
type GCRequest struct {
	DryRun bool `json:"dry_run"`
}

// GCResult summarizes a GC run.
type GCResult struct {
	Candidates int  `json:"candidates"`
	Deleted    int  `json:"deleted"`
	Failed     int  `json:"failed"`
	DryRun     bool `json:"dry_run"`
}

// StreamLine is one line of an NDJSON response body: progress output as
// it happens, then exactly one terminating Result or Error line.
type StreamLine struct {
	Log    string          `json:"log,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Error is the body of every non-2xx response.
type Error struct {
	Error string `json:"error"`
}

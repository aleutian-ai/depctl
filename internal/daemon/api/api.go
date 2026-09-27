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
	PathHealth              = "/v1/health"
	PathStatus              = "/v1/status"
	PathShutdown            = "/v1/shutdown"
	PathResolve             = "/v1/projects/resolve"
	PathPlan                = "/v1/plan"
	PathSync                = "/v1/sync"
	PathGC                  = "/v1/gc"
	PathSearch              = "/v1/search"
	PathProjectDependencies = "/v1/project-dependencies"
	PathDependencyVersion   = "/v1/dependency-version"
	PathReleaseChanges      = "/v1/release-changes"
	// PathKnowledgeStatus is deliberately not /v1/status: that route
	// already means daemon/process health (Status above). This is
	// query.Service's fleet-wide sync-coverage summary, the MCP
	// knowledge_status tool — an unrelated concept that happens to share
	// a name.
	PathKnowledgeStatus = "/v1/knowledge/status"
	PathProjectList     = "/v1/projects/list"
	PathProjectGet      = "/v1/projects/get"
	PathDescribe        = "/v1/describe"
	// PathDoctor covers every doctor check except the two that must run
	// client-side regardless (git/package-manager PATH lookups check the
	// calling user's shell PATH, not the daemon's) — see
	// DoctorResponse.NeededExecutables.
	PathDoctor = "/v1/doctor"
	// PathSyncPriority asks the currently-running sync for a project (if
	// any) to prioritize one dependency next (WATCH-020) — distinct from
	// PathSync, which always either starts a new run or queues a
	// follow-up; this one only ever affects a run already in flight.
	PathSyncPriority = "/v1/sync/priority"
	// PathSyncProgress reports one project's sync progress (SCOPE-001):
	// live counters while a run is in flight, the last run's afterwards.
	PathSyncProgress = "/v1/sync/progress"
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
	// ConfigFingerprint is config.Config.Fingerprint() as of when this
	// daemon started — config is loaded once for the daemon's whole
	// lifetime, so callers compare this against a fresh load's
	// fingerprint to detect an on-disk change that hasn't taken effect.
	ConfigFingerprint string `json:"config_fingerprint"`
	// EmbeddingState/EmbeddingDetail report the daemon's background
	// embedding-provider readiness check (WATCH-014): "unknown"/"ready"
	// (nothing to report), "checking", "pulling" (EmbeddingDetail names
	// the model), "unreachable", or "error" (EmbeddingDetail explains
	// either). Unlike ConfigFingerprint this can change over the
	// daemon's lifetime, so it's read live from the Engine on every
	// call, not cached from startup.
	EmbeddingState  string `json:"embedding_state"`
	EmbeddingDetail string `json:"embedding_detail,omitempty"`
	// VectorState/VectorDetail report the daemon's background
	// vector-backend readiness check (WATCH-015): "unknown"/"ready"
	// (nothing to report), "checking", "unreachable", or "error"
	// (VectorDetail explains either). Read live from the Engine on
	// every call, same as EmbeddingState.
	VectorState  string `json:"vector_state"`
	VectorDetail string `json:"vector_detail,omitempty"`
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
	// Syncs lists every project with a sync in flight right now.
	Syncs []ProjectSync `json:"syncs,omitempty"`
}

// ProjectSync is one project's in-flight sync, named so a human can
// recognize it.
type ProjectSync struct {
	ProjectID string `json:"project_id"`
	Root      string `json:"root"`
	SyncProgress
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
	ProjectID string `json:"project_id,omitempty"`
	// Dependency is the original single-name filter; Dependencies is the
	// set form. Both may be sent; DependencySet combines them.
	Dependency   string   `json:"dependency,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	Offline      bool     `json:"offline,omitempty"`
	Force        bool     `json:"force,omitempty"`
}

// DependencySet is every dependency name the request limits the sync to;
// empty means everything.
func (r SyncRequest) DependencySet() []string {
	set := append([]string(nil), r.Dependencies...)
	if r.Dependency != "" {
		set = append(set, r.Dependency)
	}
	return set
}

// SyncResponse carries one result per project the request covered.
type SyncResponse struct {
	Results []SyncResult `json:"results"`
}

// SyncProgress is a project's sync progress at one moment (SCOPE-001).
// Done, Failed and Total count planned actions — nearly all one per
// dependency version. With Syncing false they describe the last run, or
// are all zero if the project has never synced — except Total can also
// be legitimately zero for a project that HAS synced (every dependency
// already had a current generation elsewhere, so nothing needed
// building); Ran is what actually distinguishes the two cases.
type SyncProgress struct {
	Syncing bool `json:"syncing"`
	// Ran reports whether a sync has ever completed for this project,
	// independent of Total/Done/Failed all being zero — a run that finds
	// nothing new to sync (Total == 0, the common, correct case once
	// everything is already synced) still sets this true, so a caller
	// can tell "nothing to do" apart from "never even tried."
	Ran      bool                 `json:"ran"`
	Done     int                  `json:"done"`
	Failed   int                  `json:"failed"`
	Total    int                  `json:"total"`
	InFlight []InFlightDependency `json:"in_flight,omitempty"`
	// BATCH-001 Option D: agent-facing scope-planning information — nil
	// until at least one dependency has actually finished in this run.
	// See ObservedTiming/SyncEstimate's own doc comments for why this
	// exists and how it's meant to be used.
	Observed *ObservedTiming `json:"observed,omitempty"`
	Estimate *SyncEstimate   `json:"estimate,omitempty"`
	// Pending names every planned SYNC_VERSION dependency not yet
	// finished (done or failed) — distinct from InFlight, which is only
	// what's actively building right now. Lets a caller see the whole
	// remaining scope, not just the current worker snapshot.
	Pending []string `json:"pending,omitempty"`
}

// SyncProgressRequest asks for one project's SyncProgress.
type SyncProgressRequest struct {
	ProjectID string `json:"project_id"`
}

// InFlightDependency is one dependency being built right now, with how
// many of its chunks are embedded (ChunksTotal is 0 until its chunks are
// known).
type InFlightDependency struct {
	Name        string `json:"name"`
	ChunksDone  int    `json:"chunks_done"`
	ChunksTotal int    `json:"chunks_total"`
}

// ObservedTiming reports how long this run's completed SYNC_VERSION
// actions have actually taken (BATCH-001 Option D) — median and p90
// rather than a mean, since real timing data (STRESS-005/006) is
// heavily skewed by a handful of large dependencies dominating total
// runtime, which would make a naive average routinely misleading.
type ObservedTiming struct {
	Samples                 int     `json:"samples"`
	MedianDependencySeconds float64 `json:"median_dependency_seconds"`
	P90DependencySeconds    float64 `json:"p90_dependency_seconds"`
}

// SyncEstimate is a rough projection of how much longer the current run
// needs (median duration times how many dependencies remain Pending).
// Confidence is "low" below a real sample size, "medium"/"high" above
// it depending on how skewed the observed durations are — never treat
// RemainingSeconds as precise without checking Confidence. This is
// information only: ragctl never uses it to decide anything itself
// (BATCH-001's own non-goal) — an agent or human decides what to do
// with a large Pending list (keep waiting, request a specific
// dependency via sync_project, accept partial coverage, etc.).
type SyncEstimate struct {
	RemainingSeconds float64 `json:"remaining_seconds"`
	Confidence       string  `json:"confidence"` // "low", "medium", or "high"
}

// SyncPriorityRequest asks the currently-running sync for ProjectID, if
// any, to prioritize Dependency next (WATCH-020).
type SyncPriorityRequest struct {
	ProjectID  string `json:"project_id"`
	Dependency string `json:"dependency"`
}

// SyncPriorityResponse reports whether a sync was actually running for
// the requested project to bump — false means the caller should fall
// back to a plain sync request instead (WATCH-019's JIT-sync path).
type SyncPriorityResponse struct {
	Bumped bool `json:"bumped"`
}

// PlanRequest asks for the desired-state plan without changing anything.
type PlanRequest struct {
	ProjectID string `json:"project_id,omitempty"`
}

// GCRequest is one `ragctl gc` invocation.
type GCRequest struct {
	DryRun bool `json:"dry_run"`
	// Orphans selects GC-001/GC-002's orphan-generation eligibility path
	// (FAILED/stuck-non-terminal generations) instead of the default
	// reference-based path — never both in the same request.
	Orphans bool `json:"orphans,omitempty"`
	// SupersededDuplicates selects POINT-004's cleanup path (SUPERSEDED
	// generations that share their exact dependency+version with a
	// currently ACTIVE generation — the check-then-create race's leftover
	// duplicates) instead of the default reference-based path — never
	// combined with Orphans or the default in the same request.
	SupersededDuplicates bool `json:"superseded_duplicates,omitempty"`
}

// GCResult summarizes a GC run.
type GCResult struct {
	Candidates int  `json:"candidates"`
	Deleted    int  `json:"deleted"`
	Failed     int  `json:"failed"`
	DryRun     bool `json:"dry_run"`
}

// SearchRequest is one search_dependency_docs MCP call, or the CLI
// equivalent once one exists.
type SearchRequest struct {
	ProjectID  string `json:"project_id"`
	Text       string `json:"text"`
	Dependency string `json:"dependency,omitempty"`
	Mode       string `json:"mode,omitempty"`
	TopK       int    `json:"top_k,omitempty"`
}

// SearchResponse is SearchRequest's result.
type SearchResponse struct {
	Chunks []SearchChunk `json:"chunks"`
}

// SearchChunk is one matched chunk, mirroring query.ResultChunk's wire
// shape.
type SearchChunk struct {
	ChunkID    string  `json:"chunk_id"`
	Content    string  `json:"content"`
	Score      float32 `json:"score"`
	Ecosystem  string  `json:"ecosystem"`
	Dependency string  `json:"dependency"`
	Version    string  `json:"version"`
	Generation string  `json:"generation"`
	SourceType string  `json:"source_type"`
	Authority  int     `json:"authority"`
	TrustClass string  `json:"trust_class"`
}

// ProjectDependenciesRequest asks for a project's resolved dependencies.
type ProjectDependenciesRequest struct {
	ProjectID string `json:"project_id"`
}

// ProjectDependenciesResponse is ProjectDependenciesRequest's result.
type ProjectDependenciesResponse struct {
	Dependencies []ProjectDependency `json:"dependencies"`
}

// ProjectDependency mirrors query.ProjectDependency's wire shape.
type ProjectDependency struct {
	Ecosystem           string `json:"ecosystem"`
	Name                string `json:"name"`
	Direct              bool   `json:"direct"`
	Version             string `json:"version"`
	ResolvedBy          string `json:"resolved_by"`
	HasActiveGeneration bool   `json:"has_active_generation"`
}

// DependencyVersionRequest asks for one package's resolved version
// within a project.
type DependencyVersionRequest struct {
	ProjectID string `json:"project_id"`
	Package   string `json:"package"`
}

// DependencyVersionResponse mirrors domain.DependencyVersion's wire
// shape.
type DependencyVersionResponse struct {
	Ecosystem  string `json:"ecosystem"`
	Name       string `json:"name"`
	Direct     bool   `json:"direct"`
	Version    string `json:"version"`
	ResolvedBy string `json:"resolved_by"`
	Checksum   string `json:"checksum"`
}

// ReleaseChangesRequest asks for release-note excerpts between two
// versions of a dependency.
type ReleaseChangesRequest struct {
	Dependency string `json:"dependency"`
	From       string `json:"from"`
	To         string `json:"to"`
}

// ReleaseChangesResponse is ReleaseChangesRequest's result.
type ReleaseChangesResponse struct {
	Changes []ReleaseChange `json:"changes"`
}

// ReleaseChange mirrors query.ReleaseChange's wire shape.
type ReleaseChange struct {
	Ecosystem string `json:"ecosystem"`
	Version   string `json:"version"`
	Excerpt   string `json:"excerpt"`
}

// KnowledgeStatusResponse mirrors query.Status's wire shape — ragctl's
// fleet-wide sync-coverage summary, not to be confused with Status
// above (daemon/process health).
type KnowledgeStatusResponse struct {
	TotalProjects           int          `json:"total_projects"`
	TotalDependencies       int          `json:"total_dependencies"`
	WithActiveGeneration    int          `json:"with_active_generation"`
	WithoutActiveGeneration int          `json:"without_active_generation"`
	Projects                []ProjectRef `json:"projects"`
}

// ProjectRef mirrors query.ProjectRef's wire shape.
type ProjectRef struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

// ProjectSummary is one registered project's ID and root, for
// `ragctl project list`.
type ProjectSummary struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

// ProjectListResponse is `ragctl project list`'s result.
type ProjectListResponse struct {
	Projects []ProjectSummary `json:"projects"`
}

// ProjectGetRequest asks for one project's full detail, the work behind
// `ragctl project show` and `ragctl deps`.
type ProjectGetRequest struct {
	ProjectID string `json:"project_id"`
}

// ProjectGetResponse is ProjectGetRequest's result. HasResolution is
// false (and every field after it zero) when the project has never been
// resolved.
type ProjectGetResponse struct {
	ID            string           `json:"id"`
	Root          string           `json:"root"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	HasResolution bool             `json:"has_resolution"`
	Ecosystem     string           `json:"ecosystem,omitempty"`
	Fingerprint   string           `json:"fingerprint,omitempty"`
	Dependencies  []DependencyInfo `json:"dependencies,omitempty"`
}

// DependencyInfo is one resolved dependency, the wire shape `ragctl
// deps` renders.
type DependencyInfo struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Direct    bool   `json:"direct"`
}

// DescribeRequest is one `ragctl describe` invocation.
type DescribeRequest struct {
	Args          []string `json:"args"`
	CheckLiveness bool     `json:"check_liveness"`
}

// CheckResultWire mirrors the CLI's own CheckResult (internal/cli's
// doctor.go) — Severity is that package's int-based Severity type,
// which marshals/unmarshals as a plain number with no change needed.
type CheckResultWire struct {
	Name     string `json:"name"`
	Severity int    `json:"severity"`
	Detail   string `json:"detail"`
}

// DoctorResponse covers every doctor check except the two PATH lookups
// (git, package managers) that must run against the calling user's own
// shell PATH, which can legitimately differ from the daemon's.
// NeededExecutables is the raw data the client needs to run its own
// package-manager check locally and splice the result into the right
// place in doctor's report — see docs/internal/cli.md.
type DoctorResponse struct {
	Checks            []CheckResultWire  `json:"checks"`
	NeededExecutables []NeededExecutable `json:"needed_executables"`
}

// NeededExecutable is one package-manager executable some registered,
// resolved project needs, and how many projects need it.
type NeededExecutable struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
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
	// Kind, when set, is a stable identifier for a well-known error
	// category a client needs to recognize via errors.Is after
	// reconstructing it — a plain error message round-trip through
	// JSON otherwise silently loses the sentinel identity every
	// internal/query error (ErrProjectNotFound, ErrDependencyNotFound,
	// ErrNoActiveGeneration) depends on, breaking any client-side
	// errors.Is check against them (live-found: this made WATCH-019/020's
	// JIT-sync-on-search branch dead code against a real daemon, since it
	// only ever worked in unit tests that bypassed the HTTP boundary).
	// Empty for anything not specifically recognized.
	Kind string `json:"kind,omitempty"`
}

// Recognized Error.Kind values — see Error's own doc for why these
// exist. Intentionally a small, hand-picked set: only the sentinel
// errors an MCP tool handler actually needs to distinguish via
// errors.Is, not a general-purpose error-code system.
const (
	ErrKindProjectNotFound    = "project_not_found"
	ErrKindDependencyNotFound = "dependency_not_found"
	ErrKindNoActiveGeneration = "no_active_generation"
)

package daemon

import (
	"aleutian-ai/ragctl/internal/watch"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aleutian-ai/ragctl/internal/daemon/api"
)

// fakeEngine embeds Engine (nil) so a test only has to implement the one
// method it actually exercises — calling any other panics, which is
// exactly what should happen if a test reaches further than it meant to.
type fakeEngine struct {
	Engine
	scan func(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error)
}

func (f *fakeEngine) Scan(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error) {
	return f.scan(ctx, root, out, lockProject)
}

func newTestServer(eng Engine) *Server {
	s := New(Options{Engine: eng, Socket: "unused-in-this-test"})
	s.scheduler = NewScheduler(context.Background(), eng.Sync, eng.GC, nil)
	return s
}

// TestHandleResolveBoundedByMaxActionDuration is the direct regression
// test for scan's missing ceiling: before handleResolve wrapped its call
// to Engine.Scan in maxActionDuration, a hung resolver had no bound at
// all — sync/GC already did (Scheduler.execute), scan didn't, since it
// doesn't go through the scheduler.
func TestHandleResolveBoundedByMaxActionDuration(t *testing.T) {
	original := maxActionDuration
	maxActionDuration = 50 * time.Millisecond
	defer func() { maxActionDuration = original }()

	eng := &fakeEngine{
		scan: func(ctx context.Context, root string, out io.Writer, lockProject func(string) func()) ([]string, error) {
			<-ctx.Done() // never returns on its own
			return nil, ctx.Err()
		},
	}
	s := newTestServer(eng)

	body, _ := json.Marshal(api.ResolveRequest{Root: "/tmp/whatever"})
	req := httptest.NewRequest(http.MethodPost, api.PathResolve, bytes.NewReader(body))
	w := httptest.NewRecorder()

	start := time.Now()
	s.handleResolve(w, req)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("handleResolve took %s against a hung Scan, want it bounded by maxActionDuration (50ms)", elapsed)
	}

	// Streamed responses (see stream.go) always answer 200 and carry the
	// outcome in the last NDJSON line, not the HTTP status — the real
	// assertion is that the last line is an error, not a result.
	dec := json.NewDecoder(w.Body)
	var last api.StreamLine
	for {
		var line api.StreamLine
		if err := dec.Decode(&line); err != nil {
			break
		}
		last = line
	}
	if last.Error == "" {
		t.Errorf("last stream line = %+v, want an Error for a Scan that never completed", last)
	}
}

// exportMem0Engine answers only what handleExportMem0 asks for.
type exportMem0Engine struct {
	Engine
	resp api.ExportMem0Response
	err  error
}

func (e *exportMem0Engine) ExportMem0(ctx context.Context, req api.ExportMem0Request, out io.Writer) (api.ExportMem0Response, error) {
	io.WriteString(out, "exporting\n")
	return e.resp, e.err
}

// TestHandleExportMem0StreamsResult confirms POST /v1/export/mem0
// streams progress and a terminal result like every other long-running
// route (handleSync, handleResolve, handleGC) — MEM0-001's daemon-owned
// design depends on this matching that exact existing shape.
func TestHandleExportMem0StreamsResult(t *testing.T) {
	eng := &exportMem0Engine{resp: api.ExportMem0Response{Results: []api.ExportMem0Result{{Dependency: "google.golang.org/protobuf", Pushed: 3}}}}
	s := newTestServer(eng)

	body, _ := json.Marshal(api.ExportMem0Request{ProjectID: "proj-1"})
	req := httptest.NewRequest(http.MethodPost, api.PathExportMem0, bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleExportMem0(w, req)

	dec := json.NewDecoder(w.Body)
	var sawLog bool
	var result api.ExportMem0Response
	var sawResult bool
	for {
		var line api.StreamLine
		if err := dec.Decode(&line); err != nil {
			break
		}
		if line.Log != "" {
			sawLog = true
		}
		if line.Result != nil {
			if err := json.Unmarshal(line.Result, &result); err != nil {
				t.Fatalf("decode result line: %v", err)
			}
			sawResult = true
		}
		if line.Error != "" {
			t.Fatalf("unexpected error line: %s", line.Error)
		}
	}
	if !sawLog {
		t.Error("expected at least one progress log line")
	}
	if !sawResult {
		t.Fatal("expected a terminal result line")
	}
	if len(result.Results) != 1 || result.Results[0].Pushed != 3 {
		t.Errorf("result = %+v, want one dependency with Pushed=3", result)
	}
}

// TestHandleExportMem0RejectsEmptyProjectID confirms the handler
// validates project_id before ever reaching the engine — matching
// handleResolve's own empty-root check.
func TestHandleExportMem0RejectsEmptyProjectID(t *testing.T) {
	s := newTestServer(&exportMem0Engine{})

	body, _ := json.Marshal(api.ExportMem0Request{})
	req := httptest.NewRequest(http.MethodPost, api.PathExportMem0, bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleExportMem0(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// exportMultiEngine answers ExportGraphiti/ExportCognee for their own
// streaming-shape tests, mirroring exportMem0Engine.
type exportMultiEngine struct {
	Engine
	graphitiResp api.ExportGraphitiResponse
	cogneeResp   api.ExportCogneeResponse
}

func (e *exportMultiEngine) ExportGraphiti(ctx context.Context, req api.ExportGraphitiRequest, out io.Writer) (api.ExportGraphitiResponse, error) {
	io.WriteString(out, "exporting\n")
	return e.graphitiResp, nil
}

func (e *exportMultiEngine) ExportCognee(ctx context.Context, req api.ExportCogneeRequest, out io.Writer) (api.ExportCogneeResponse, error) {
	io.WriteString(out, "exporting\n")
	return e.cogneeResp, nil
}

// TestHandleExportGraphitiCogneeStreamResults confirms both remaining
// epic-65 routes stream progress and a terminal result the same way
// handleExportMem0 does.
func TestHandleExportGraphitiCogneeStreamResults(t *testing.T) {
	eng := &exportMultiEngine{
		graphitiResp: api.ExportGraphitiResponse{Results: []api.ExportGraphitiResult{{Dependency: "dep-a", Pushed: 1}}},
		cogneeResp:   api.ExportCogneeResponse{Results: []api.ExportCogneeResult{{Dependency: "dep-a", Pushed: 1}}},
	}
	s := newTestServer(eng)

	cases := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
		body    any
	}{
		{"graphiti", api.PathExportGraphiti, s.handleExportGraphiti, api.ExportGraphitiRequest{ProjectID: "proj-1"}},
		{"cognee", api.PathExportCognee, s.handleExportCognee, api.ExportCogneeRequest{ProjectID: "proj-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			w := httptest.NewRecorder()
			tc.handler(w, req)

			dec := json.NewDecoder(w.Body)
			var sawResult bool
			for {
				var line api.StreamLine
				if err := dec.Decode(&line); err != nil {
					break
				}
				if line.Error != "" {
					t.Fatalf("unexpected error line: %s", line.Error)
				}
				if line.Result != nil {
					sawResult = true
				}
			}
			if !sawResult {
				t.Fatalf("%s: expected a terminal result line", tc.name)
			}
		})
	}
}

// statusEngine answers only what handleStatus and syncActivity ask for.
type statusEngine struct {
	Engine
	projects []watch.Project
}

func (e *statusEngine) Status(context.Context) (api.Status, error)        { return api.Status{}, nil }
func (e *statusEngine) Projects(context.Context) ([]watch.Project, error) { return e.projects, nil }

// TestStatusReportsInFlightSyncsWithProjectNames is SCOPE-001's status
// surface: a running sync appears in `ragctl status` with its project
// named, its counts and its in-flight dependency; nothing running means
// no "syncs" at all.
func TestStatusReportsInFlightSyncsWithProjectNames(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		opts.Progress.SetTotal(10)
		opts.Progress.Begin("example.com/big")
		opts.Progress.SetChunks("example.com/big", 40, 400)
		close(started)
		<-release
		return api.SyncResult{ProjectID: projectID}, nil
	}
	eng := &statusEngine{projects: []watch.Project{{ID: "proj_a", Root: "/work/app"}}}
	s := New(Options{Engine: eng, Socket: "unused-in-this-test"})
	s.scheduler = NewScheduler(context.Background(), run, noGC, nil)

	getStatus := func() api.Status {
		w := httptest.NewRecorder()
		s.handleStatus(w, httptest.NewRequest(http.MethodGet, api.PathStatus, nil))
		var st api.Status
		if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		return st
	}

	if st := getStatus(); len(st.Syncs) != 0 {
		t.Errorf("idle status has syncs %+v, want none", st.Syncs)
	}

	waiter := s.scheduler.Request("proj_a", SyncOptions{}, nil)
	<-started
	st := getStatus()
	if len(st.Syncs) != 1 {
		t.Fatalf("syncs = %+v, want exactly one", st.Syncs)
	}
	got := st.Syncs[0]
	if got.Root != "/work/app" || !got.Syncing || got.Total != 10 || len(got.InFlight) != 1 || got.InFlight[0].ChunksDone != 40 {
		t.Errorf("sync = %+v, want /work/app, syncing, total 10, big at 40 chunks", got)
	}

	close(release)
	awaitResult(t, waiter)
	s.scheduler.Wait()
	if st := getStatus(); len(st.Syncs) != 0 {
		t.Errorf("status after the run has syncs %+v, want none", st.Syncs)
	}
}

// TestHandleSyncProgressReadsSchedulerState: the endpoint returns the
// scheduler's live counters and, for a project that never synced, zeros.
func TestHandleSyncProgressReadsSchedulerState(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	run := func(_ context.Context, _ *BuildCoordinator, projectID string, opts SyncOptions, _ io.Writer) (api.SyncResult, error) {
		opts.Progress.SetTotal(7)
		close(started)
		<-release
		return api.SyncResult{ProjectID: projectID}, nil
	}
	s := New(Options{Engine: &statusEngine{}, Socket: "unused-in-this-test"})
	s.scheduler = NewScheduler(context.Background(), run, noGC, nil)

	ask := func(projectID string) api.SyncProgress {
		body, _ := json.Marshal(api.SyncProgressRequest{ProjectID: projectID})
		w := httptest.NewRecorder()
		s.handleSyncProgress(w, httptest.NewRequest(http.MethodPost, api.PathSyncProgress, bytes.NewReader(body)))
		var got api.SyncProgress
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}

	if got := ask("proj_a"); got.Syncing || got.Total != 0 {
		t.Errorf("never-synced = %+v, want zeros", got)
	}
	waiter := s.scheduler.Request("proj_a", SyncOptions{}, nil)
	<-started
	if got := ask("proj_a"); !got.Syncing || got.Total != 7 {
		t.Errorf("in flight = %+v, want syncing with total 7", got)
	}
	if got := ask("proj_other"); got.Syncing {
		t.Errorf("a different project = %+v, want it untouched", got)
	}
	close(release)
	awaitResult(t, waiter)
	s.scheduler.Wait()
}

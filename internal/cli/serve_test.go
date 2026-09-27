package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/query"
)

// fakeStartupScanTrigger and fakeStartupQueryService are minimal
// mcp.ScanTrigger/mcp.QueryService fakes scoped to what startupScan
// itself calls (Status only) — everything else panics if exercised,
// since startupScan never should.

type fakeStartupScanTrigger struct {
	ids     []string
	summary string
	err     error
}

func (f *fakeStartupScanTrigger) ScanProject(ctx context.Context, root string, progress func(line string)) ([]string, string, error) {
	return f.ids, f.summary, f.err
}

type fakeStartupQueryService struct {
	status query.Status
	err    error
}

func (f *fakeStartupQueryService) Status(ctx context.Context) (query.Status, error) {
	return f.status, f.err
}
func (f *fakeStartupQueryService) GetProjectDependencies(ctx context.Context, projectID string) ([]query.ProjectDependency, error) {
	panic("not used by startupScan")
}
func (f *fakeStartupQueryService) GetDependencyVersion(ctx context.Context, projectID, pkg string) (domain.DependencyVersion, error) {
	panic("not used by startupScan")
}
func (f *fakeStartupQueryService) GetReleaseChanges(ctx context.Context, dependency, from, to string) ([]query.ReleaseChange, error) {
	panic("not used by startupScan")
}
func (f *fakeStartupQueryService) SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error) {
	panic("not used by startupScan")
}

func TestStartupScanNilTrigger(t *testing.T) {
	var buf bytes.Buffer
	startupScan(context.Background(), &buf, nil, nil)
	if buf.Len() != 0 {
		t.Errorf("nil ScanTrigger: got output %q, want none", buf.String())
	}
}

func TestStartupScanFailureIsNonFatal(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{err: errors.New("boom")}
	startupScan(context.Background(), &buf, scan, &fakeStartupQueryService{})
	if !strings.Contains(buf.String(), "startup scan failed") || !strings.Contains(buf.String(), "boom") {
		t.Errorf("scan error output = %q, want a logged, non-fatal message naming the error", buf.String())
	}
}

func TestStartupScanNoProjectsSkipsStatus(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{ids: nil, summary: "no projects found under .\n"}
	q := &fakeStartupQueryService{err: errors.New("Status must not be called with zero projects")}
	startupScan(context.Background(), &buf, scan, q)
	if !strings.Contains(buf.String(), "no projects found") {
		t.Errorf("output = %q, want the scan summary printed", buf.String())
	}
	if strings.Contains(buf.String(), "must not be called") {
		t.Errorf("startupScan called Status() despite zero registered project IDs: %q", buf.String())
	}
}

func TestStartupScanReportsStatusSummary(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{ids: []string{"proj_1"}, summary: "registered proj_1\n"}
	q := &fakeStartupQueryService{status: query.Status{
		TotalProjects: 1, TotalDependencies: 42, WithActiveGeneration: 10, WithoutActiveGeneration: 32,
	}}
	startupScan(context.Background(), &buf, scan, q)
	out := buf.String()
	if !strings.Contains(out, "registered proj_1") {
		t.Errorf("output = %q, want the scan summary printed", out)
	}
	if !strings.Contains(out, "1 project(s), 42 dependencies, 10/42 already synced") {
		t.Errorf("output = %q, want a status summary with the fake's numbers", out)
	}
}

func TestStartupScanStatusFailureIsNonFatal(t *testing.T) {
	var buf bytes.Buffer
	scan := &fakeStartupScanTrigger{ids: []string{"proj_1"}}
	q := &fakeStartupQueryService{err: errors.New("daemon unreachable")}
	startupScan(context.Background(), &buf, scan, q)
	if !strings.Contains(buf.String(), "startup status check failed") || !strings.Contains(buf.String(), "daemon unreachable") {
		t.Errorf("status error output = %q, want a logged, non-fatal message naming the error", buf.String())
	}
}

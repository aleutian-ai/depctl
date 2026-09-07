package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
)

func describeTestStores(t *testing.T) (*bboltstore.Store, *badgerstore.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := bboltstore.Open(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("bboltstore.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	badgerStore, err := badgerstore.Open(filepath.Join(dir, "badger"))
	if err != nil {
		t.Fatalf("badgerstore.Open: %v", err)
	}
	t.Cleanup(func() { badgerStore.Close() })

	return store, badgerStore
}

const describeManifestYAML = `apiVersion: ragctl.dev/v1alpha1
kind: KnowledgePackage
metadata:
  name: %[1]s
match:
  ecosystems: [node]
  packages: [%[1]s]
version:
  strategy: none
sources:
  - id: repository
    type: git
    url: https://example.com/repo
    ref: HEAD
    authority: 100
`

func describeTestRegistry(t *testing.T, packages ...string) *registry.Registry {
	t.Helper()
	dir := t.TempDir()
	for i, pkg := range packages {
		name := filepath.Join(dir, "manifest"+string(rune('a'+i))+".yaml")
		content := fmt.Sprintf(describeManifestYAML, pkg)
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	reg, err := registry.NewLoader(dir, "").Load(context.Background())
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

// seedActiveGeneration creates a promoted generation for (eco, pkg,
// version) with the given manifest counts and a backend replica, so
// buildPackageEntry has something real to report beyond "never synced."
func seedActiveGeneration(t *testing.T, store *bboltstore.Store, badgerStore *badgerstore.Store, eco domain.Ecosystem, pkg, version string, chunkCount int) string {
	t.Helper()
	ctx := context.Background()
	dep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: eco, Name: pkg}, Version: version}

	gen, err := generation.Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("generation.Create: %v", err)
	}
	if err := store.PromoteGeneration(ctx, gen, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}

	m := generation.Manifest{ID: gen.ID, Dependency: dep, ChunkCount: chunkCount, ObjectCount: chunkCount, CreatedAt: time.Now()}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := badgerStore.PutManifest(ctx, gen.ID, data); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	replica := domain.BackendReplica{GenerationID: gen.ID, BackendName: "qdrant", Status: "complete", PointCount: chunkCount, UpdatedAt: time.Now()}
	if err := store.PutBackendReplica(ctx, replica); err != nil {
		t.Fatalf("PutBackendReplica: %v", err)
	}
	return gen.ID
}

func TestBuildReportCoversActiveManifestOnlyAndUnmappedPackages(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := describeTestStores(t)
	reg := describeTestRegistry(t, "pkg-active", "pkg-manifest-only")

	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemNode, Package: "pkg-active", Version: "1.0.0", Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference pkg-active: %v", err)
	}
	genID := seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, "pkg-active", "1.0.0", 42)

	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemNode, Package: "pkg-manifest-only", Version: "1.0.0", Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference pkg-manifest-only: %v", err)
	}

	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: "proj_1", Ecosystem: domain.EcosystemNode, Package: "pkg-unmapped", Version: "1.0.0", Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference pkg-unmapped: %v", err)
	}

	report, err := buildReport(ctx, store, badgerStore, reg, "qdrant", "")
	if err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	if len(report.Packages) != 3 {
		t.Fatalf("got %d packages, want 3: %+v", len(report.Packages), report.Packages)
	}

	byName := map[string]PackageEntry{}
	for _, p := range report.Packages {
		byName[p.Package] = p
	}

	active := byName["pkg-active"]
	if !active.ManifestMatch {
		t.Error("pkg-active: ManifestMatch = false, want true")
	}
	if active.ActiveVersion != "1.0.0" {
		t.Errorf("pkg-active: ActiveVersion = %q, want 1.0.0", active.ActiveVersion)
	}
	if active.GenerationID != genID {
		t.Errorf("pkg-active: GenerationID = %q, want %q", active.GenerationID, genID)
	}
	if active.ChunkCount != 42 {
		t.Errorf("pkg-active: ChunkCount = %d, want 42", active.ChunkCount)
	}
	if active.ReplicaStatus != "complete" || active.ReplicaPoints != 42 {
		t.Errorf("pkg-active: replica = %s/%d, want complete/42", active.ReplicaStatus, active.ReplicaPoints)
	}
	if len(active.Sources) != 1 || active.Sources[0].TrustClass != domain.TrustRepository {
		t.Errorf("pkg-active: Sources = %+v, want one repository-trust source", active.Sources)
	}

	manifestOnly := byName["pkg-manifest-only"]
	if !manifestOnly.ManifestMatch {
		t.Error("pkg-manifest-only: ManifestMatch = false, want true")
	}
	if manifestOnly.ActiveVersion != "" {
		t.Errorf("pkg-manifest-only: ActiveVersion = %q, want empty (never synced)", manifestOnly.ActiveVersion)
	}

	unmapped := byName["pkg-unmapped"]
	if unmapped.ManifestMatch {
		t.Error("pkg-unmapped: ManifestMatch = true, want false (no registry entry)")
	}
	if len(unmapped.Sources) != 0 {
		t.Errorf("pkg-unmapped: Sources = %+v, want empty", unmapped.Sources)
	}
}

func TestBuildReportFilterScopesToOnePackage(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := describeTestStores(t)
	reg := describeTestRegistry(t, "pkg-active")
	seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, "pkg-active", "1.0.0", 7)

	report, err := buildReport(ctx, store, badgerStore, reg, "qdrant", "node/pkg-active")
	if err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	if len(report.Packages) != 1 || report.Packages[0].Package != "pkg-active" {
		t.Fatalf("got %+v, want exactly pkg-active", report.Packages)
	}
}

func TestParsePackageFilterRejectsMissingSlash(t *testing.T) {
	if _, _, err := parsePackageFilter("no-slash-here"); err == nil {
		t.Error("parsePackageFilter(no slash) = nil error, want error")
	}
}

func TestReportJSONRoundTrips(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := describeTestStores(t)
	reg := describeTestRegistry(t, "pkg-active")
	seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, "pkg-active", "1.0.0", 3)
	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: "p1", Ecosystem: domain.EcosystemNode, Package: "pkg-active", Version: "1.0.0", Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}

	report, err := buildReport(ctx, store, badgerStore, reg, "qdrant", "")
	if err != nil {
		t.Fatalf("buildReport: %v", err)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Packages) != 1 || got.Packages[0].Package != "pkg-active" {
		t.Errorf("round-tripped report = %+v", got)
	}
}

// TestDescribeHTMLIsWellFormedAndContainsPackageNames checks the
// generated HTML is well-formed enough to trust (balanced <html>/<table>
// tags) and names every package, without pulling in an HTML-parsing
// dependency this codebase doesn't otherwise need — see DESC-001's
// simplicity constraint against new dependencies for a static template.
func TestDescribeHTMLIsWellFormedAndContainsPackageNames(t *testing.T) {
	report := Report{
		GeneratedAt: time.Now(),
		Packages: []PackageEntry{
			{Ecosystem: domain.EcosystemNode, Package: "pkg-active", ActiveVersion: "1.0.0", ChunkCount: 5, ManifestMatch: true},
			{Ecosystem: domain.EcosystemNode, Package: "pkg-unmapped"},
		},
	}
	var buf bytes.Buffer
	if err := describeHTMLTemplate.Execute(&buf, report); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	out := buf.String()

	for _, tag := range []string{"<html>", "</html>", "<table>", "</table>", "<!doctype html>"} {
		if !strings.Contains(out, tag) {
			t.Errorf("HTML output missing expected tag %q", tag)
		}
	}
	for _, name := range []string{"pkg-active", "pkg-unmapped"} {
		if !strings.Contains(out, name) {
			t.Errorf("HTML output missing package name %q", name)
		}
	}
}

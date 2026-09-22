package generation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/source/git"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ragctl-test", "GIT_AUTHOR_EMAIL=ragctl-test@example.com",
		"GIT_COMMITTER_NAME=ragctl-test", "GIT_COMMITTER_EMAIL=ragctl-test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newFixtureRepo creates a repo with a Markdown README and a documented
// Go package, commits and tags it v1.0.0, and returns the repo directory.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Widget\n\nA small example package.\n")
	writeFile(t, dir, "widget.go", "// Package widget does something.\npackage widget\n\n// Do does something.\nfunc Do() {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	runGit(t, dir, "tag", "v1.0.0")
	return dir
}

func testStores(t *testing.T) (*bbolt.Store, *badger.Store) {
	t.Helper()
	bs, err := bbolt.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { bs.Close() })

	dds, err := badger.Open(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { dds.Close() })

	return bs, dds
}

func testDependency() domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"},
		Version:    "v1.0.0",
	}
}

func TestCreatePersistsPlannedGeneration(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := testStores(t)

	gen, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gen.State != domain.GenPlanned {
		t.Errorf("gen.State = %s, want %s", gen.State, domain.GenPlanned)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenPlanned {
		t.Errorf("GetGeneration.State = %s, want %s", got.State, domain.GenPlanned)
	}

	data, err := badgerStore.GetManifest(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if len(data) == 0 {
		t.Error("manifest is empty")
	}
}

func TestCreateGeneratesDistinctIDs(t *testing.T) {
	ctx := context.Background()
	store, badgerStore := testStores(t)

	a, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	b, err := Create(ctx, store, badgerStore, testDependency())
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("two generations for the same dependency version got the same ID: %s", a.ID)
	}
}

func TestBuildEndToEnd(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenIndexing {
		t.Errorf("gen.State = %s, want %s", got.State, domain.GenIndexing)
	}

	manifest, err := getManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		t.Fatalf("getManifest: %v", err)
	}
	if manifest.ObjectCount < 2 {
		t.Errorf("manifest.ObjectCount = %d, want >= 2 (markdown + go doc)", manifest.ObjectCount)
	}
	if manifest.ChunkCount == 0 {
		t.Error("manifest.ChunkCount = 0, want > 0")
	}
	if manifest.ObjectsCreated != manifest.ObjectCount {
		t.Errorf("manifest.ObjectsCreated = %d, want %d (nothing to reuse yet)", manifest.ObjectsCreated, manifest.ObjectCount)
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	if len(chunks) != manifest.ChunkCount {
		t.Errorf("ListGenerationChunks returned %d, manifest says %d", len(chunks), manifest.ChunkCount)
	}
}

// TestBuildSkipsNestedGoModuleBoundary reproduces the failure found live
// syncing google-cloud-go (STRESS-005): a real repo's root module can sit
// alongside hundreds of separately-versioned sibling modules (each its
// own go.mod) in the same git checkout — google-cloud-go's root module
// resolves to a single file, doc.go, but the repo also contains 216
// nested go.mod boundaries. Before this fix, normalizeSources walked
// straight past those boundaries, normalizing every sibling module's
// content as if it belonged to the dependency being synced — both
// wildly inflating chunk volume (13399 directories walked instead of 23
// for google-cloud-go) and mislabeling unrelated packages' docs as this
// dependency's own.
func TestBuildSkipsNestedGoModuleBoundary(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	baseline := func(t *testing.T) int {
		store, badgerStore := testStores(t)
		gitCache := git.NewCache(t.TempDir())
		repoDir := newFixtureRepo(t)
		dep := testDependency()
		sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
		gen, err := Create(ctx, store, badgerStore, dep)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
			t.Fatalf("Build: %v", err)
		}
		manifest, err := getManifest(ctx, badgerStore, gen.ID)
		if err != nil {
			t.Fatalf("getManifest: %v", err)
		}
		return manifest.ObjectCount
	}

	baselineCount := baseline(t)

	// Same fixture, plus a nested Go module (its own go.mod, its own
	// exported, documented function) inside a subdirectory — the shape
	// google-cloud-go's storage/, bigquery/, etc. take relative to its
	// root module.
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())
	repoDir := newFixtureRepo(t)
	writeFile(t, repoDir, "sibling/go.mod", "module example.com/widget/sibling\n\ngo 1.21\n")
	writeFile(t, repoDir, "sibling/sibling.go", "// Package sibling is a wholly separate module.\npackage sibling\n\n// Other does something else entirely.\nfunc Other() {}\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "add sibling module")
	runGit(t, repoDir, "tag", "-f", "v1.0.0")

	dep := testDependency()
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build: %v", err)
	}
	manifest, err := getManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		t.Fatalf("getManifest: %v", err)
	}

	if manifest.ObjectCount != baselineCount {
		t.Errorf("ObjectCount = %d with a nested sibling module present, want %d (identical to the baseline without it — the sibling module's content must never be normalized as this dependency's own)", manifest.ObjectCount, baselineCount)
	}
}

func TestBuildAcquisitionFailureMarksGenerationFailed(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No "v1.0.0"-shaped tag exists under this template.
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "does-not-exist-${version}", Authority: 100}}
	err = Build(ctx, gen, sources, gitCache, store, badgerStore)
	if err == nil {
		t.Fatal("Build succeeded, want acquisition failure")
	}
	if !errors.Is(err, ErrAcquisition) {
		t.Errorf("Build error = %v, want wrapping ErrAcquisition", err)
	}

	got, getErr := store.GetGeneration(ctx, gen.ID)
	if getErr != nil {
		t.Fatalf("GetGeneration: %v", getErr)
	}
	if got.State != domain.GenFailed {
		t.Errorf("gen.State = %s, want %s", got.State, domain.GenFailed)
	}
	if got.Error == "" {
		t.Error("gen.Error is empty, want a stored failure message")
	}

	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("ListGenerationChunks = %d chunks, want 0 after acquisition failure", len(chunks))
	}
}

func TestBuildContentReuseAcrossGenerations(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t)
	dep := testDependency()
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Authority: 100}}

	gen1, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create gen1: %v", err)
	}
	if err := Build(ctx, gen1, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build gen1: %v", err)
	}
	manifest1, err := getManifest(ctx, badgerStore, gen1.ID)
	if err != nil {
		t.Fatalf("getManifest gen1: %v", err)
	}

	gen2, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create gen2: %v", err)
	}
	if err := Build(ctx, gen2, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build gen2: %v", err)
	}
	manifest2, err := getManifest(ctx, badgerStore, gen2.ID)
	if err != nil {
		t.Fatalf("getManifest gen2: %v", err)
	}

	if manifest2.ObjectsReused != manifest1.ObjectCount {
		t.Errorf("gen2.ObjectsReused = %d, want %d (all of gen1's objects, unchanged content)", manifest2.ObjectsReused, manifest1.ObjectCount)
	}
	if manifest2.ObjectsCreated != 0 {
		t.Errorf("gen2.ObjectsCreated = %d, want 0", manifest2.ObjectsCreated)
	}

	// Now change one file and rebuild at a new version; exactly one new
	// object should be created, the rest reused despite every object's
	// source commit having changed.
	writeFile(t, repoDir, "README.md", "# Widget\n\nAn updated example package.\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "update readme")
	runGit(t, repoDir, "tag", "v1.0.1")

	// Build only mirrors on demand (EnsureMirror is a no-op once cloned);
	// refreshing the mirror's tags is the caller's job, same as a real
	// planner would do before rebuilding.
	repoPath, err := gitCache.EnsureMirror(ctx, repoDir)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if err := gitCache.FetchTags(ctx, repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}

	dep2 := dep
	dep2.Version = "v1.0.1"
	gen3, err := Create(ctx, store, badgerStore, dep2)
	if err != nil {
		t.Fatalf("Create gen3: %v", err)
	}
	if err := Build(ctx, gen3, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build gen3: %v", err)
	}
	manifest3, err := getManifest(ctx, badgerStore, gen3.ID)
	if err != nil {
		t.Fatalf("getManifest gen3: %v", err)
	}
	if manifest3.ObjectsCreated != 1 {
		t.Errorf("gen3.ObjectsCreated = %d, want 1 (only the changed README)", manifest3.ObjectsCreated)
	}
	if manifest3.ObjectsReused != manifest3.ObjectCount-1 {
		t.Errorf("gen3.ObjectsReused = %d, want %d", manifest3.ObjectsReused, manifest3.ObjectCount-1)
	}
}

// TestBuildDuplicateContentWithinOneGenerationCountsChunksOnce covers a
// GEN-003 case TestBuildContentReuseAcrossGenerations doesn't: two
// distinct source files with byte-identical content in the SAME build.
// resolveObjectIdentity dedups them to the same obj.ID, so their
// content-derived chunk IDs collide too — indexObjects must count and
// store each distinct chunk once, not once per source file, or
// manifest.ChunkCount overstates what's actually staged in Badger and
// validate.Structural fails on every real build with any duplicated file
// (vendored LICENSE copies, generated boilerplate, etc.).
func TestBuildDuplicateContentWithinOneGenerationCountsChunksOnce(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "README.md", "# Widget\n\nA small example package.\n")
	writeFile(t, dir, "docs/README.md", "# Widget\n\nA small example package.\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	runGit(t, dir, "tag", "v1.0.0")

	dep := testDependency()
	sources := []registry.Source{{ID: "repository", Type: "git", URL: dir, Ref: "v${version}", Authority: 100}}

	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build: %v", err)
	}

	manifest, err := getManifest(ctx, badgerStore, gen.ID)
	if err != nil {
		t.Fatalf("getManifest: %v", err)
	}
	chunks, err := badgerStore.ListGenerationChunks(ctx, gen.ID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}

	if manifest.ChunkCount != len(chunks) {
		t.Errorf("manifest.ChunkCount = %d, actual staged chunks = %d; want equal (VAL-001 checks this exact invariant)", manifest.ChunkCount, len(chunks))
	}
	if manifest.ObjectsReused != 1 {
		t.Errorf("manifest.ObjectsReused = %d, want 1 (the duplicate docs/README.md)", manifest.ObjectsReused)
	}
}

// TestTrustClassForSourceType covers SEC-001's classification rules:
// git/godoc sources derive directly from the package's own repository;
// website/github-releases are registry-declared official sources; an
// unrecognized (including empty, i.e. no registry match) source type
// falls back to unknown rather than a zero value Validate() would reject.
func TestTrustClassForSourceType(t *testing.T) {
	cases := []struct {
		sourceType string
		want       domain.TrustClass
	}{
		{"git", domain.TrustRepository},
		{"godoc", domain.TrustRepository},
		{"website", domain.TrustOfficial},
		{"github-releases", domain.TrustOfficial},
		{"", domain.TrustUnknown},
		{"something-unrecognized", domain.TrustUnknown},
	}
	for _, c := range cases {
		if got := TrustClassForSourceType(c.sourceType); got != c.want {
			t.Errorf("TrustClassForSourceType(%q) = %q, want %q", c.sourceType, got, c.want)
		}
	}
}

// TestAppendAttributedSetsTrustClassFromSourceType is the ticket's own
// example case: an object derived from a registry git source gets
// TrustRepository.
func TestAppendAttributedSetsTrustClassFromSourceType(t *testing.T) {
	dep := testDependency()
	source := registry.Source{ID: "repository", Type: "git", URL: "https://example.com/repo", Authority: 100}
	objs := []domain.KnowledgeObject{{ID: "obj_1", SourceURI: "https://example.com/repo/README.md"}}

	got := appendAttributed(nil, dep, source, objs)
	if len(got) != 1 {
		t.Fatalf("appendAttributed returned %d objects, want 1", len(got))
	}
	if got[0].TrustClass != domain.TrustRepository {
		t.Errorf("TrustClass = %q, want %q", got[0].TrustClass, domain.TrustRepository)
	}
}

// TestBuildScopesToSourceSubdir is the POINT-002 regression: a source
// with a Subdir indexes exactly that directory's module (a monorepo
// submodule's own docs), not the repo root, and a Subdir that doesn't
// exist at the resolved commit fails acquisition instead of silently
// indexing nothing.
func TestBuildScopesToSourceSubdir(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	repoDir := newFixtureRepo(t)
	writeFile(t, repoDir, "go.mod", "module example.com/widget\n\ngo 1.21\n")
	writeFile(t, repoDir, "sibling/go.mod", "module example.com/widget/sibling\n\ngo 1.21\n")
	writeFile(t, repoDir, "sibling/sibling.go", "// Package sibling is a wholly separate module.\npackage sibling\n\n// Other does something else entirely.\nfunc Other() {}\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "add sibling module")
	runGit(t, repoDir, "tag", "-f", "v1.0.0")

	build := func(t *testing.T, subdir string) (Manifest, error) {
		store, badgerStore := testStores(t)
		gitCache := git.NewCache(t.TempDir())
		sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "v${version}", Subdir: subdir, Authority: 100}}
		gen, err := Create(ctx, store, badgerStore, testDependency())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
			return Manifest{}, err
		}
		m, err := getManifest(ctx, badgerStore, gen.ID)
		if err != nil {
			t.Fatalf("getManifest: %v", err)
		}
		return m, nil
	}

	root, err := build(t, "")
	if err != nil {
		t.Fatalf("root build: %v", err)
	}
	sibling, err := build(t, "sibling")
	if err != nil {
		t.Fatalf("sibling build: %v", err)
	}
	if sibling.ObjectCount == 0 {
		t.Error("sibling subdir indexed nothing, want its own package docs")
	}
	// Root: README + widget package doc + Do. Sibling: package doc + Other
	// only — none of the root module's content.
	if root.ObjectCount != 3 || sibling.ObjectCount != 2 {
		t.Errorf("ObjectCount root=%d sibling=%d, want 3 and 2", root.ObjectCount, sibling.ObjectCount)
	}

	if _, err := build(t, "does/not/exist"); !errors.Is(err, ErrAcquisition) {
		t.Errorf("missing subdir error = %v, want ErrAcquisition", err)
	}
}

func TestRefCandidates(t *testing.T) {
	root := registry.Source{ID: "r", Ref: "v${version}"}
	sub := registry.Source{ID: "r", Ref: "billing/v${version}"}
	cases := []struct {
		name    string
		source  registry.Source
		version string
		want    []string
	}{
		{"root module tag", root, "v1.2.3", []string{"v1.2.3"}},
		{"submodule tag", sub, "v1.5.0", []string{"billing/v1.5.0"}},
		{"pre-release and deprecated suffixes are part of the tag", sub, "v0.1.0-deprecated", []string{"billing/v0.1.0-deprecated"}},
		{"+incompatible is a path marker, not part of the tag", root, "v2.0.0+incompatible", []string{"v2.0.0"}},
		{"pseudo-version names a commit", sub, "v0.0.0-20210226163009-5ac0b6a4141c", []string{"5ac0b6a4141c"}},
		{"pre-release pseudo-version names a commit", root, "v1.2.4-0.20200804184101-5ec99f83aff1", []string{"5ec99f83aff1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := refCandidates(tc.source, tc.version)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("refCandidates = %v (err %v), want %v", got, err, tc.want)
			}
		})
	}
	if _, err := refCandidates(registry.Source{ID: "r"}, "v1.0.0"); err == nil {
		t.Error("a source with no ref template must be an error")
	}
}

// versionedMonorepo builds a repo whose sub/ module changed between two
// tagged releases and again afterwards on the default branch, returning
// the repo dir and the commit of the first release.
func versionedMonorepo(t *testing.T) (dir, firstCommit string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "sub/go.mod", "module example.com/mono/sub\n\ngo 1.21\n")
	writeFile(t, dir, "sub/sub.go", "// Package sub says: release one.\npackage sub\n\n// Say speaks.\nfunc Say() {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "release one")
	runGit(t, dir, "tag", "sub/v1.0.0")
	firstCommit = gitOutput(t, dir, "rev-parse", "HEAD")

	writeFile(t, dir, "sub/sub.go", "// Package sub says: release two.\npackage sub\n\n// Say speaks.\nfunc Say() {}\n")
	runGit(t, dir, "commit", "-q", "-am", "release two")
	runGit(t, dir, "tag", "sub/v2.0.0")

	writeFile(t, dir, "sub/sub.go", "// Package sub says: unreleased head.\npackage sub\n\n// Say speaks.\nfunc Say() {}\n")
	runGit(t, dir, "commit", "-q", "-am", "unreleased work on main")
	return dir, firstCommit
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func chunkText(t *testing.T, badgerStore *badger.Store, genID string) string {
	t.Helper()
	chunks, err := badgerStore.ListGenerationChunks(context.Background(), genID)
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	var all strings.Builder
	for _, c := range chunks {
		all.Write(c.Content)
		all.WriteString("\n")
	}
	return all.String()
}

func buildSubdirAt(t *testing.T, gitCache *git.Cache, repoDir, version string) (string, error) {
	t.Helper()
	store, badgerStore := testStores(t)
	dep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/mono/sub"}, Version: version}
	gen, err := Create(context.Background(), store, badgerStore, dep)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sources := []registry.Source{{ID: "repository", Type: "git", URL: repoDir, Ref: "sub/v${version}", Subdir: "sub", Authority: 0}}
	if err := Build(context.Background(), gen, sources, gitCache, store, badgerStore); err != nil {
		return "", err
	}
	return chunkText(t, badgerStore, gen.ID), nil
}

// TestBuildIsVersionExact is the core of the fix: each version indexes
// the content at its own tag, not whatever the default branch holds now.
func TestBuildIsVersionExact(t *testing.T) {
	requireGit(t)
	repo, _ := versionedMonorepo(t)
	cache := git.NewCache(t.TempDir())

	one, err := buildSubdirAt(t, cache, repo, "v1.0.0")
	if err != nil {
		t.Fatalf("v1.0.0: %v", err)
	}
	two, err := buildSubdirAt(t, cache, repo, "v2.0.0")
	if err != nil {
		t.Fatalf("v2.0.0: %v", err)
	}
	if !strings.Contains(one, "release one") || strings.Contains(one, "release two") || strings.Contains(one, "unreleased") {
		t.Errorf("v1.0.0 content = %q, want only release one", one)
	}
	if !strings.Contains(two, "release two") || strings.Contains(two, "unreleased") {
		t.Errorf("v2.0.0 content = %q, want release two and nothing from unreleased main", two)
	}
}

func TestBuildResolvesAPseudoVersionToItsCommit(t *testing.T) {
	requireGit(t)
	repo, first := versionedMonorepo(t)
	cache := git.NewCache(t.TempDir())

	text, err := buildSubdirAt(t, cache, repo, "v0.0.0-20200101000000-"+first[:12])
	if err != nil {
		t.Fatalf("pseudo-version: %v", err)
	}
	if !strings.Contains(text, "release one") || strings.Contains(text, "release two") {
		t.Errorf("pseudo-version content = %q, want the first release's commit", text)
	}
}

// TestBuildNeverFallsBackToTheBranchHead: a version with no tag fails to
// acquire, naming the ref it looked for — it does not silently index
// main under that version's label.
func TestBuildNeverFallsBackToTheBranchHead(t *testing.T) {
	requireGit(t)
	repo, _ := versionedMonorepo(t)

	_, err := buildSubdirAt(t, git.NewCache(t.TempDir()), repo, "v9.9.9")
	if !errors.Is(err, ErrAcquisition) || !strings.Contains(err.Error(), "sub/v9.9.9") {
		t.Errorf("err = %v, want an acquisition error naming sub/v9.9.9", err)
	}
}

// TestBuildRefreshesTagsForAVersionReleasedAfterTheMirrorWasCached: the
// cache never re-fetches on its own, so a newer release must trigger one
// tag refresh instead of failing as "missing".
func TestBuildRefreshesTagsForAVersionReleasedAfterTheMirrorWasCached(t *testing.T) {
	requireGit(t)
	repo, _ := versionedMonorepo(t)
	cache := git.NewCache(t.TempDir())

	if _, err := buildSubdirAt(t, cache, repo, "v1.0.0"); err != nil { // clones the mirror
		t.Fatalf("v1.0.0: %v", err)
	}
	writeFile(t, repo, "sub/sub.go", "// Package sub says: release three.\npackage sub\n\n// Say speaks.\nfunc Say() {}\n")
	runGit(t, repo, "commit", "-q", "-am", "release three")
	runGit(t, repo, "tag", "sub/v3.0.0")

	text, err := buildSubdirAt(t, cache, repo, "v3.0.0")
	if err != nil {
		t.Fatalf("v3.0.0 after the mirror was cached: %v", err)
	}
	if !strings.Contains(text, "release three") {
		t.Errorf("v3.0.0 content = %q, want release three", text)
	}
}

func TestRefCandidatesExpandsRefTemplates(t *testing.T) {
	got, err := refCandidates(registry.Source{RefTemplates: []string{"v${version}", "${version}", "pkg@${version}"}}, "v1.2.3")
	want := []string{"v1.2.3", "1.2.3", "pkg@1.2.3"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("refCandidates = %v (err %v), want %v", got, err, want)
	}
}

// TestBuildTriesEveryRefTemplateUntilOneResolves is REG-012's real-git
// proof: a repo tagged only in the "<package>@<version>" convention still
// resolves, because every template is tried and verified before use.
func TestBuildTriesEveryRefTemplateUntilOneResolves(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "index.js", "// nothing\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "release")
	runGit(t, dir, "tag", "widget@3.4.3") // not "v3.4.3" or "3.4.3" — only the monorepo convention

	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())
	sources := []registry.Source{{ID: "repository", Type: "git", URL: dir, RefTemplates: []string{"v${version}", "${version}", "widget@${version}"}, Authority: 0}}
	dep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "widget"}, Version: "3.4.3"}
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatal(err)
	}
	if err := Build(ctx, gen, sources, gitCache, store, badgerStore); err != nil {
		t.Fatalf("Build: %v, want the widget@3.4.3 template to resolve after v3.4.3 and 3.4.3 both miss", err)
	}
}

func TestBuildFailsCleanlyWhenNoRefTemplateResolves(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, "index.js", "// nothing\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "release")
	// deliberately no tag at all

	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())
	sources := []registry.Source{{ID: "repository", Type: "git", URL: dir, RefTemplates: []string{"v${version}", "${version}"}, Authority: 0}}
	dep := domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemNode, Name: "widget"}, Version: "9.9.9"}
	gen, err := Create(ctx, store, badgerStore, dep)
	if err != nil {
		t.Fatal(err)
	}
	err = Build(ctx, gen, sources, gitCache, store, badgerStore)
	if !errors.Is(err, ErrAcquisition) || !strings.Contains(err.Error(), "v9.9.9 or 9.9.9") {
		t.Errorf("err = %v, want an acquisition error naming both untried refs, no branch-head fallback", err)
	}
}

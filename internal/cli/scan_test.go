package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

// sharedGoCache is a build/module cache directory outside any test's
// isolated HOME, reused across the whole test binary run. Go's build cache
// creates read-only subdirectories by design; if GOCACHE lived inside a
// per-test t.TempDir() (as it would by default once isolateEnv overrides
// HOME), t.TempDir()'s own cleanup can fail to remove them.
var sharedGoCache = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "ragctl-test-gocache-")
	if err != nil {
		panic(err)
	}
	return dir
})

// requireGo skips the test if the go toolchain isn't on PATH, and points it
// at the local fixture only — no module proxy fetch, no go.sum network
// verification — since our fixtures never declare external requires.
func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOCACHE", sharedGoCache())
	t.Setenv("GOPATH", sharedGoCache())
	// Go telemetry writes counter files under HOME, sometimes via a
	// detached background uploader process that can still be running
	// after the `go` command returns — that races t.TempDir()'s cleanup
	// of the isolated HOME set by isolateEnv. GOTELEMETRY=off should
	// suppress it, but redirect GOTELEMETRYDIR too, to the same
	// never-cleaned shared dir as GOCACHE/GOPATH, so a stray write can't
	// race any per-test cleanup either way. See https://go.dev/doc/telemetry.
	t.Setenv("GOTELEMETRY", "off")
	t.Setenv("GOTELEMETRYDIR", sharedGoCache())
}

func writeGoMod(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod in %s: %v", dir, err)
	}
}

// withSupportedEcosystem temporarily marks eco as supported for the
// duration of the test, restoring the real (currently empty) set after.
func withSupportedEcosystem(t *testing.T, eco domain.Ecosystem) {
	t.Helper()
	original := supportedEcosystems
	supportedEcosystems = map[domain.Ecosystem]bool{eco: true}
	t.Cleanup(func() { supportedEcosystems = original })
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s in %s: %v", name, dir, err)
	}
}

// TestScanConcurrentSameProjectDoesNotRace exercises the real
// integration, not just Scheduler.LockProject in isolation: two `ragctl
// scan` invocations discovering the same project at once must not race
// each other's PutProject/PutResolution — see
// docs/scratch/action-controller-proposal.md.
func TestScanConcurrentSameProjectDoesNotRace(t *testing.T) {
	isolateEnv(t)
	withSupportedEcosystem(t, domain.EcosystemGo)
	requireGo(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	writeGoMod(t, filepath.Join(root, "service-go"), "module example.com/service-go\n\ngo 1.21\n")

	rootCmd := NewRootCmd()
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	const concurrent = 4
	errs := make([]error, concurrent)
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := NewRootCmd()
			cmd.SetArgs([]string{"scan", root})
			cmd.SetOut(new(bytes.Buffer))
			errs[i] = cmd.Execute()
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("scan %d: %v", i, err)
		}
	}

	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d persisted projects after %d concurrent scans of the same root, want exactly 1: %+v", len(projects), concurrent, projects)
	}

	res, err := store.GetResolution(t.Context(), projects[0].ID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if len(res.Dependencies) != 0 {
		t.Errorf("resolution = %+v, want the fixture's zero-dependency result intact (not partially overwritten)", res)
	}
}

func TestScanFixtureTreeAllUnsupported(t *testing.T) {
	isolateEnv(t)
	useRealRagctlBinary(t)
	// Neither fixture uses go.mod, a Python manifest, or package.json: go,
	// python, and node are genuinely supported ecosystems
	// (internal/resolver/golang, internal/resolver/python,
	// internal/resolver/node), so those fixtures would actually resolve
	// rather than stay unsupported.
	root := t.TempDir()
	touch(t, filepath.Join(root, "service-rust", "Cargo.toml"))
	touch(t, filepath.Join(root, "service-java", "pom.xml"))

	rootCmd := NewRootCmd()
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	rootCmd = NewRootCmd()
	rootCmd.SetArgs([]string{"scan", root})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if !strings.Contains(out.String(), "discovered 2, new 0, existing 0, unsupported 2") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}
}

func TestScanRegistersSupportedEcosystem(t *testing.T) {
	isolateEnv(t)
	withSupportedEcosystem(t, domain.EcosystemGo)
	requireGo(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	writeGoMod(t, filepath.Join(root, "service-go"), "module example.com/service-go\n\ngo 1.21\n")
	touch(t, filepath.Join(root, "service-java", "pom.xml")) // stays unsupported

	rootCmd := NewRootCmd()
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	rootCmd = NewRootCmd()
	rootCmd.SetArgs([]string{"scan", root})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(out.String(), "discovered 2, new 1, existing 0, unsupported 1") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}

	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before reading the store directly.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d persisted projects, want 1: %+v", len(projects), projects)
	}

	res, err := store.GetResolution(t.Context(), projects[0].ID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if res.Ecosystem != domain.EcosystemGo {
		t.Errorf("resolution Ecosystem = %q, want go", res.Ecosystem)
	}
}

func TestScanRegistersPythonEcosystem(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	touch(t, filepath.Join(root, "service-java", "pom.xml")) // stays unsupported
	if err := os.MkdirAll(filepath.Join(root, "service-py"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "service-py", "requirements.txt"), []byte("pydantic==2.11.7\n"), 0o644); err != nil {
		t.Fatalf("write requirements.txt: %v", err)
	}

	scanCmd := NewRootCmd()
	scanCmd.SetArgs([]string{"scan", root})
	var out bytes.Buffer
	scanCmd.SetOut(&out)
	if err := scanCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(out.String(), "discovered 2, new 1, existing 0, unsupported 1") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}

	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before reading the store directly.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d persisted projects, want 1: %+v", len(projects), projects)
	}

	res, err := store.GetResolution(t.Context(), projects[0].ID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if res.Ecosystem != domain.EcosystemPython {
		t.Errorf("resolution Ecosystem = %q, want python", res.Ecosystem)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "pydantic" {
		t.Errorf("Dependencies = %+v, want just pydantic", res.Dependencies)
	}
}

func TestScanRegistersNodeEcosystem(t *testing.T) {
	isolateEnv(t)
	runInitForTest(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	touch(t, filepath.Join(root, "service-java", "pom.xml")) // stays unsupported
	writeFile(t, filepath.Join(root, "web"), "package.json", `{"name":"web"}`)
	writeFile(t, filepath.Join(root, "web"), "package-lock.json", `{
  "lockfileVersion": 3,
  "packages": {
    "": { "dependencies": { "left-pad": "^1.3.0" } },
    "node_modules/left-pad": { "version": "1.3.0" }
  }
}`)

	scanCmd := NewRootCmd()
	scanCmd.SetArgs([]string{"scan", root})
	var out bytes.Buffer
	scanCmd.SetOut(&out)
	if err := scanCmd.Execute(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !strings.Contains(out.String(), "discovered 2, new 1, existing 0, unsupported 1") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}

	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before reading the store directly.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d persisted projects, want 1: %+v", len(projects), projects)
	}

	res, err := store.GetResolution(t.Context(), projects[0].ID)
	if err != nil {
		t.Fatalf("GetResolution: %v", err)
	}
	if res.Ecosystem != domain.EcosystemNode {
		t.Errorf("resolution Ecosystem = %q, want node", res.Ecosystem)
	}
	if len(res.Dependencies) != 1 || res.Dependencies[0].Dependency.Name != "left-pad" {
		t.Errorf("Dependencies = %+v, want just left-pad", res.Dependencies)
	}
}

func TestScanTwiceIsIdempotent(t *testing.T) {
	isolateEnv(t)
	withSupportedEcosystem(t, domain.EcosystemGo)
	requireGo(t)
	useRealRagctlBinary(t)

	root := t.TempDir()
	writeGoMod(t, filepath.Join(root, "service-go"), "module example.com/service-go\n\ngo 1.21\n")

	rootCmd := NewRootCmd()
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	runScanOnce := func() string {
		rootCmd := NewRootCmd()
		rootCmd.SetArgs([]string{"scan", root})
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return out.String()
	}

	first := runScanOnce()
	if !strings.Contains(first, "new 1, existing 0") {
		t.Errorf("first scan: unexpected summary:\n%s", first)
	}

	second := runScanOnce()
	if !strings.Contains(second, "new 0, existing 1") {
		t.Errorf("second scan: unexpected summary:\n%s", second)
	}

	// The daemon `scan` auto-started still holds the control store's
	// exclusive lock; release it before reading the store directly.
	stopRunningDaemon(t)
	store, err := openControlStore()
	if err != nil {
		t.Fatalf("openControlStore: %v", err)
	}
	defer store.Close()
	projects, err := store.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d persisted projects after two scans, want exactly 1 (no duplicates): %+v", len(projects), projects)
	}
}

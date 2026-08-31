package golang

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// requireGo skips the test if the go toolchain isn't on PATH — go list
// tests need the real toolchain, not a mock.
func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
}

// offlineEnv makes `go list` resolve entirely from local files: no module
// proxy fetch, no go.sum verification against a network source.
func offlineEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
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

func TestListModulesSimpleProject(t *testing.T) {
	requireGo(t)
	offlineEnv(t)

	root := t.TempDir()
	writeGoMod(t, root, "module example.com/simple\n\ngo 1.21\n")

	modules, err := listModules(context.Background(), root)
	if err != nil {
		t.Fatalf("listModules: %v", err)
	}
	if len(modules) != 1 || !modules[0].Main {
		t.Fatalf("modules = %+v, want exactly the main module", modules)
	}
}

func TestListModulesLocalReplace(t *testing.T) {
	requireGo(t)
	offlineEnv(t)

	base := t.TempDir()
	depDir := filepath.Join(base, "foolocal")
	writeGoMod(t, depDir, "module example.com/foo\n\ngo 1.21\n")

	root := filepath.Join(base, "main")
	writeGoMod(t, root, "module example.com/main\n\ngo 1.21\n\nrequire example.com/foo v0.0.0\n\nreplace example.com/foo => ../foolocal\n")

	modules, err := listModules(context.Background(), root)
	if err != nil {
		t.Fatalf("listModules: %v", err)
	}

	var foo *goModule
	for i := range modules {
		if modules[i].Path == "example.com/foo" {
			foo = &modules[i]
		}
	}
	if foo == nil {
		t.Fatalf("example.com/foo not found in %+v", modules)
	}
	if foo.Replace == nil || foo.Replace.Path != "../foolocal" || foo.Replace.Version != "" {
		t.Errorf("Replace = %+v, want local path replace with empty version", foo.Replace)
	}
}

// TestListModulesVendorDirectory reproduces a real-world failure found by
// scanning kubernetes/moby-scale repos: a vendored project (vendor/
// present, go.mod go>=1.14) makes `go list -m -json all` fail outright
// under Go's default vendor-mode auto-detection, unless -mod=mod is
// forced. listModules sets GOFLAGS=-mod=mod for exactly this reason.
func TestListModulesVendorDirectory(t *testing.T) {
	requireGo(t)
	t.Setenv("GOPROXY", "off") // offlineEnv not used: GOFLAGS is set by listModules itself here

	base := t.TempDir()
	depDir := filepath.Join(base, "foolocal")
	writeGoMod(t, depDir, "module example.com/foo\n\ngo 1.21\n")

	root := filepath.Join(base, "main")
	writeGoMod(t, root, "module example.com/main\n\ngo 1.21\n\nrequire example.com/foo v0.0.0\n\nreplace example.com/foo => ../foolocal\n")

	vendorDir := filepath.Join(root, "vendor")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatalf("mkdir vendor: %v", err)
	}
	// Deliberately inconsistent with go.mod's replace directive — this is
	// what actually triggers the vendor-mode error, not mere presence of
	// the directory.
	modulesTxt := "# example.com/foo v0.0.0\n## explicit\nexample.com/foo\n"
	if err := os.WriteFile(filepath.Join(vendorDir, "modules.txt"), []byte(modulesTxt), 0o644); err != nil {
		t.Fatalf("write vendor/modules.txt: %v", err)
	}

	modules, err := listModules(context.Background(), root)
	if err != nil {
		t.Fatalf("listModules: %v (GOFLAGS=-mod=mod should bypass vendor-mode errors)", err)
	}
	if len(modules) != 2 {
		t.Errorf("modules = %+v, want main + example.com/foo", modules)
	}
}

// TestListModulesDoesNotDownloadToolchain reproduces a real-world failure
// found scanning moby: a go.mod requesting a newer Go than what's
// installed makes the toolchain try to download that release over the
// network by default, which can eat most of defaultListTimeout before
// failing. listModules sets GOTOOLCHAIN=local so this fails immediately
// instead.
func TestListModulesDoesNotDownloadToolchain(t *testing.T) {
	requireGo(t)
	t.Setenv("GOPROXY", "off")

	root := t.TempDir()
	writeGoMod(t, root, "module example.com/newtoolchain\n\ngo 1.99.0\n")

	start := time.Now()
	_, err := listModules(context.Background(), root)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for a go.mod requiring an uninstalled toolchain, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("listModules took %v, want a fast failure (GOTOOLCHAIN=local should prevent a network download attempt)", elapsed)
	}
}

// TestListModulesWorkspaceMode reproduces a real-world failure found
// scanning kubernetes: a go.work file in an ancestor directory puts `go
// list` in workspace mode, where GOFLAGS=-mod=mod (needed for
// TestListModulesVendorDirectory) is itself invalid ("-mod may only be set
// to readonly or vendor when in workspace mode"). We resolve per go.mod,
// not per workspace, so listModules sets GOWORK=off to sidestep this.
func TestListModulesWorkspaceMode(t *testing.T) {
	requireGo(t)
	t.Setenv("GOPROXY", "off")

	base := t.TempDir()
	modDir := filepath.Join(base, "mod1")
	writeGoMod(t, modDir, "module example.com/mod1\n\ngo 1.21\n")
	if err := os.WriteFile(filepath.Join(base, "go.work"), []byte("go 1.21\n\nuse ./mod1\n"), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
	}

	modules, err := listModules(context.Background(), modDir)
	if err != nil {
		t.Fatalf("listModules: %v (GOWORK=off should sidestep the workspace-mode conflict)", err)
	}
	if len(modules) != 1 || !modules[0].Main {
		t.Fatalf("modules = %+v, want exactly the main module", modules)
	}
}

func TestListModulesContextCancellation(t *testing.T) {
	requireGo(t)
	offlineEnv(t)

	root := t.TempDir()
	writeGoMod(t, root, "module example.com/simple\n\ngo 1.21\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := listModules(ctx, root)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("listModules took %v after cancellation, expected prompt return", elapsed)
	}
}

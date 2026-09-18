package gopackages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/symbolgraph"
)

// writeFile writes content to path, creating parent directories as
// needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fixtureModule builds a small real, buildable Go module tree: a
// dependency module ("example.com/dep") exporting a plain function and
// a type with a method, plus an app module ("example.com/app") that
// locally-replaces and calls into it. Returns the app module's root.
func fixtureModule(t *testing.T) string {
	t.Helper()
	base := t.TempDir()

	depRoot := filepath.Join(base, "dep")
	writeFile(t, filepath.Join(depRoot, "go.mod"), "module example.com/dep\n\ngo 1.21\n")
	writeFile(t, filepath.Join(depRoot, "dep.go"), `package dep

// Greet returns a greeting.
func Greet(name string) string { return "hello " + name }

// Client is a fixture type with a pointer-receiver method, mirroring a
// real dependency's typical API shape (e.g. bbolt's *Tx).
type Client struct{}

// Connect returns a new connection.
func (c *Client) Connect(addr string) error { return nil }

// Ping implements Connector.
func (c *Client) Ping() error { return nil }

// Connector is an interface a caller might hold instead of a concrete
// *Client, to exercise resolution through an interface-typed value.
type Connector interface {
	Ping() error
}
`)

	appRoot := filepath.Join(base, "app")
	writeFile(t, filepath.Join(appRoot, "go.mod"),
		"module example.com/app\n\ngo 1.21\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	writeFile(t, filepath.Join(appRoot, "main.go"), `package main

import "example.com/dep"

func main() {
	dep.Greet("world")

	c := &dep.Client{}
	c.Connect("localhost")

	var conn dep.Connector = c
	conn.Ping()

	internalHelper()
}

func internalHelper() {}
`)

	return appRoot
}

// findInFile returns the 1-indexed line and column of the first
// occurrence of needle in the file at path, for building CallSite
// fixtures without hardcoding brittle line numbers.
func findInFile(t *testing.T, path, needle string) (line, column int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if col := strings.Index(l, needle); col >= 0 {
			return i + 1, col + 1
		}
	}
	t.Fatalf("needle %q not found in %s", needle, path)
	return 0, 0
}

func TestResolvePlainFunctionCall(t *testing.T) {
	root := fixtureModule(t)
	line, col := findInFile(t, filepath.Join(root, "main.go"), "Greet(")

	p := New(root)
	ref, ok, err := p.Resolve(context.Background(), symbolgraph.CallSite{File: "main.go", Line: line, Column: col})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve ok = false, want true for an external function call")
	}
	if ref.Module != "example.com/dep" {
		t.Errorf("ref.Module = %q, want example.com/dep", ref.Module)
	}
	if ref.QualifiedName != "Greet" {
		t.Errorf("ref.QualifiedName = %q, want Greet", ref.QualifiedName)
	}
	if ref.Ecosystem != "go" {
		t.Errorf("ref.Ecosystem = %q, want go", ref.Ecosystem)
	}
}

func TestResolveMethodCallViaSelector(t *testing.T) {
	root := fixtureModule(t)
	line, col := findInFile(t, filepath.Join(root, "main.go"), "Connect(")

	p := New(root)
	ref, ok, err := p.Resolve(context.Background(), symbolgraph.CallSite{File: "main.go", Line: line, Column: col})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve ok = false, want true for an external method call")
	}
	if ref.Module != "example.com/dep" {
		t.Errorf("ref.Module = %q, want example.com/dep", ref.Module)
	}
	if ref.QualifiedName != "(*Client).Connect" {
		t.Errorf("ref.QualifiedName = %q, want (*Client).Connect", ref.QualifiedName)
	}
}

func TestResolveMethodCallViaInterfaceValue(t *testing.T) {
	root := fixtureModule(t)
	line, col := findInFile(t, filepath.Join(root, "main.go"), "Ping()")

	p := New(root)
	ref, ok, err := p.Resolve(context.Background(), symbolgraph.CallSite{File: "main.go", Line: line, Column: col})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve ok = false, want true for a call through an interface-typed value")
	}
	if ref.Module != "example.com/dep" {
		t.Errorf("ref.Module = %q, want example.com/dep", ref.Module)
	}
	// Resolved against the static interface method, not the concrete
	// *Client — go/types can't know the runtime type, and shouldn't have
	// to: the interface itself is defined in the external module, which
	// is exactly the fact this ticket needs to establish.
	if ref.QualifiedName != "Connector.Ping" {
		t.Errorf("ref.QualifiedName = %q, want Connector.Ping", ref.QualifiedName)
	}
}

// fixtureMultiPackageModule builds a dependency module with TWO
// packages — "example.com/multidep" (root) and its subpackage
// "example.com/multidep/sub" — plus an app module calling into the
// subpackage. This is the shape that exposes a Module-vs-package-path
// bug: types.Package only ever exposes a symbol's own package path
// ("example.com/multidep/sub"), never the module it belongs to
// ("example.com/multidep") — the overwhelming majority of real Go
// dependencies (e.g. github.com/stretchr/testify/assert, whose module
// is github.com/stretchr/testify) are structured exactly this way.
func fixtureMultiPackageModule(t *testing.T) string {
	t.Helper()
	base := t.TempDir()

	depRoot := filepath.Join(base, "multidep")
	writeFile(t, filepath.Join(depRoot, "go.mod"), "module example.com/multidep\n\ngo 1.21\n")
	writeFile(t, filepath.Join(depRoot, "sub", "sub.go"), `package sub

// Helper is called from the subpackage, not the module root.
func Helper() string { return "helper" }
`)

	appRoot := filepath.Join(base, "app")
	writeFile(t, filepath.Join(appRoot, "go.mod"),
		"module example.com/app\n\ngo 1.21\n\nrequire example.com/multidep v0.0.0\n\nreplace example.com/multidep => ../multidep\n")
	writeFile(t, filepath.Join(appRoot, "main.go"), `package main

import "example.com/multidep/sub"

func main() {
	sub.Helper()
}
`)

	return appRoot
}

func TestResolveModuleIsRootNotSubpackagePath(t *testing.T) {
	root := fixtureMultiPackageModule(t)
	line, col := findInFile(t, filepath.Join(root, "main.go"), "Helper()")

	p := New(root)
	ref, ok, err := p.Resolve(context.Background(), symbolgraph.CallSite{File: "main.go", Line: line, Column: col})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("Resolve ok = false, want true for a call into a dependency's subpackage")
	}
	if ref.Module != "example.com/multidep" {
		t.Errorf("ref.Module = %q, want the MODULE root example.com/multidep, not the package path", ref.Module)
	}
	if ref.Package != "sub" {
		t.Errorf("ref.Package = %q, want sub", ref.Package)
	}
	if ref.QualifiedName != "Helper" {
		t.Errorf("ref.QualifiedName = %q, want Helper", ref.QualifiedName)
	}
}

func TestResolveInternalCallSiteReturnsNotOK(t *testing.T) {
	root := fixtureModule(t)
	line, col := findInFile(t, filepath.Join(root, "main.go"), "internalHelper()")

	p := New(root)
	ref, ok, err := p.Resolve(context.Background(), symbolgraph.CallSite{File: "main.go", Line: line, Column: col})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Errorf("Resolve ok = true for an internal call site, want false (got %+v)", ref)
	}
}

func TestResolveCompileErrorReturnsTypedError(t *testing.T) {
	root := fixtureModule(t)
	writeFile(t, filepath.Join(root, "broken.go"), "package main\n\nfunc broken() { this is not valid go }\n")

	p := New(root)
	_, ok, err := p.Resolve(context.Background(), symbolgraph.CallSite{File: "broken.go", Line: 3, Column: 1})
	if err == nil {
		t.Fatal("Resolve err = nil, want a typed error for a package that fails to compile")
	}
	if ok {
		t.Error("Resolve ok = true alongside a compile error, want false")
	}
}

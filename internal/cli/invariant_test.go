package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// allowedStoreOpeners is the closed list of top-level functions allowed
// to open control.db/badger/ directly — everything else must reach the
// stores through ensureDaemon (ADR-011). This is WATCH-011's invariant
// test: a fresh direct-open call site reintroduces the exact class of
// lock-contention bug this whole daemon-mode migration was built to
// eliminate (docs/scratch/daemon-mcp-topology.md,
// docs/scratch/action-controller-proposal.md).
//
// If this test starts failing because you added a new direct store
// access, the fix is almost always to route through ensureDaemon
// instead of widening this list — see docs/internal/cli.md's notes for
// why each of these six is here and the others aren't.
var allowedStoreOpeners = map[string]bool{
	// The shared helpers themselves (internal/cli/store.go) — they
	// define the direct-open calls this test looks for, not additional
	// callers of them.
	"openControlStore":             true,
	"openDataStore":                true,
	"openControlStoreForDaemonRun": true,
	// runInit creates the stores, so it must open them before a daemon
	// can exist to hand that job to.
	"runInit": true,
	// runDaemonRun is the daemon itself becoming the one store owner.
	"runDaemonRun": true,
	// runDoctorDirect is doctor's deliberate no-daemon fallback: dial
	// only, never autostart, so diagnosing a stopped or broken daemon
	// never has the side effect of starting one — see
	// docs/internal/cli.md's "doctor" notes.
	"runDoctorDirect": true,
}

// directOpenIdents are bare-identifier calls that open a store.
var directOpenIdents = map[string]bool{
	"openControlStore":             true,
	"openDataStore":                true,
	"openControlStoreForDaemonRun": true,
}

// directOpenSelectors are pkg.Func calls that open a store, keyed by the
// import alias this package actually uses for each.
var directOpenSelectors = map[string]map[string]bool{
	"bboltstore":  {"Open": true, "OpenWithTimeout": true},
	"badgerstore": {"Open": true},
}

// TestOnlyAllowedFunctionsOpenStoresDirectly parses every non-test file
// in this package and fails if any function outside allowedStoreOpeners
// calls a direct-open function — enforced by inspecting the AST, not by
// trusting that everyone remembers the rule (WATCH-011).
func TestOnlyAllowedFunctionsOpenStoresDirectly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	fset := token.NewFileSet()
	found := false
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			funcName := fn.Name.Name

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, isDirectOpen := describeCallee(call)
				if !isDirectOpen {
					return true
				}
				found = true
				if !allowedStoreOpeners[funcName] {
					t.Errorf("%s: %s calls %s directly, but %s is not in allowedStoreOpeners — "+
						"store-touching commands must go through ensureDaemon (ADR-011, WATCH-011), "+
						"not open the stores themselves", file, funcName, callee, funcName)
				}
				return true
			})
		}
	}

	if !found {
		// This test is only useful if it's actually exercising real
		// call sites — if the six allowed functions get refactored away
		// entirely, this failing loudly (rather than passing vacuously)
		// is the right outcome: it means the allow-list itself is stale
		// and needs a human to reconcile it, not silently rot into a
		// test that can never fail.
		t.Fatal("found no direct-open call sites at all — allowedStoreOpeners is likely stale; reconcile it against the current code")
	}
}

// describeCallee reports whether call is one of the direct-open
// functions this invariant is about, and a human-readable name for it.
func describeCallee(call *ast.CallExpr) (name string, isDirectOpen bool) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if directOpenIdents[fn.Name] {
			return fn.Name, true
		}
	case *ast.SelectorExpr:
		pkgIdent, ok := fn.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		if methods, ok := directOpenSelectors[pkgIdent.Name]; ok && methods[fn.Sel.Name] {
			return pkgIdent.Name + "." + fn.Sel.Name, true
		}
	}
	return "", false
}

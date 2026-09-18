// Package gopackages implements symbolgraph.SymbolProvider for Go using
// golang.org/x/tools/go/packages — a one-shot, type-checked load, not an
// LSP session or a persistent index (GRAPH-003, epic 42).
package gopackages

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"

	"aleutian-ai/ragctl/internal/symbolgraph"
)

// packagesLoadMode is everything Resolve needs from a load: enough type
// information to map an identifier to its defining types.Object, plus
// enough module metadata to tell an external symbol from an internal
// one.
const packagesLoadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedImports |
	packages.NeedDeps |
	packages.NeedTypes |
	packages.NeedSyntax |
	packages.NeedTypesInfo |
	packages.NeedModule

// Provider resolves Go call sites to external symbols using a
// type-checked go/packages load scoped to the call site's own package —
// deterministic, no long-running server process.
type Provider struct {
	moduleRoot string
}

var _ symbolgraph.SymbolProvider = (*Provider)(nil)

// New returns a Provider that loads packages relative to moduleRoot (the
// directory containing the project's go.mod).
func New(moduleRoot string) *Provider {
	return &Provider{moduleRoot: moduleRoot}
}

// Resolve loads the Go package containing site.File, finds the
// identifier at site's position, and maps it to the types.Object it
// refers to. Returns (ExternalSymbolRef{}, false, nil) for a symbol
// defined in the same module as moduleRoot, or one the position doesn't
// land on.
func (p *Provider) Resolve(ctx context.Context, site symbolgraph.CallSite) (symbolgraph.ExternalSymbolRef, bool, error) {
	dir := filepath.Dir(filepath.Join(p.moduleRoot, site.File))
	pattern := "./" + relPattern(p.moduleRoot, dir)

	cfg := &packages.Config{
		Context: ctx,
		Dir:     p.moduleRoot,
		Mode:    packagesLoadMode,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return symbolgraph.ExternalSymbolRef{}, false, fmt.Errorf("gopackages: load %s: %w", pattern, err)
	}
	if len(pkgs) == 0 {
		return symbolgraph.ExternalSymbolRef{}, false, fmt.Errorf("gopackages: no package loaded for %s", pattern)
	}
	homeModule := ""
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			return symbolgraph.ExternalSymbolRef{}, false, fmt.Errorf("gopackages: package %s has errors: %v", pkg.PkgPath, pkg.Errors)
		}
		if pkg.Module != nil {
			homeModule = pkg.Module.Path
		}
	}
	// packagePathToModule maps every loaded package's import path (roots
	// and every transitive dependency reachable via NeedDeps/NeedImports)
	// to its owning module's path — types.Package only ever exposes the
	// package's own import path, never its module, and for any
	// multi-package dependency (the common case: e.g.
	// "github.com/stretchr/testify/assert" belongs to module
	// "github.com/stretchr/testify") those two differ. A package with no
	// module (the standard library, or a GOPATH-mode dependency) is left
	// unmapped, and refFromObject falls back to the package path itself.
	packagePathToModule := map[string]string{}
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if pkg.Module != nil {
			packagePathToModule[pkg.PkgPath] = pkg.Module.Path
		}
	})

	absFile := filepath.Join(p.moduleRoot, site.File)
	for _, pkg := range pkgs {
		astFile, fset := findASTFile(pkg, absFile)
		if astFile == nil {
			continue
		}
		obj := identAt(fset, astFile, pkg.TypesInfo, site.Line, site.Column)
		if obj == nil {
			return symbolgraph.ExternalSymbolRef{}, false, nil
		}
		return refFromObject(obj, homeModule, packagePathToModule, fset)
	}
	return symbolgraph.ExternalSymbolRef{}, false, fmt.Errorf("gopackages: file %s not found in loaded package(s)", site.File)
}

// relPattern returns dir relative to root, in slash form, for use as a
// go/packages load pattern.
func relPattern(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

func findASTFile(pkg *packages.Package, absFile string) (*ast.File, *token.FileSet) {
	for _, f := range pkg.Syntax {
		pos := pkg.Fset.Position(f.Pos())
		if samePath(pos.Filename, absFile) {
			return f, pkg.Fset
		}
	}
	return nil, nil
}

func samePath(a, b string) bool {
	aAbs, errA := filepath.Abs(a)
	bAbs, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return aAbs == bAbs
}

// identAt finds the identifier at line/column (1-indexed) in file and
// returns the types.Object it refers to — via TypesInfo.Uses for a
// plain identifier, or TypesInfo.Selections for a selector expression
// (method/field access) — or nil if the position doesn't land on an
// identifiable expression.
func identAt(fset *token.FileSet, file *ast.File, info *types.Info, line, column int) types.Object {
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil || line < 1 || line > tokenFile.LineCount() {
		return nil
	}
	pos := tokenFile.LineStart(line) + token.Pos(column-1)

	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	for _, node := range path {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if sel, ok := info.Selections[n]; ok {
				return sel.Obj()
			}
			if obj := info.Uses[n.Sel]; obj != nil {
				return obj
			}
		case *ast.Ident:
			if obj := info.Uses[n]; obj != nil {
				return obj
			}
		}
	}
	return nil
}

// refFromObject builds an ExternalSymbolRef from obj, or reports
// (zero, false, nil) if obj belongs to the home module (an internal
// symbol) or has no package at all (a builtin). fset is the shared
// FileSet from the load that produced obj, used to resolve SourceFile.
// packagePathToModule maps obj's package path to its owning module —
// see the comment where it's built in Resolve for why that mapping
// can't be derived from obj/pkg alone.
func refFromObject(obj types.Object, homeModule string, packagePathToModule map[string]string, fset *token.FileSet) (symbolgraph.ExternalSymbolRef, bool, error) {
	pkg := obj.Pkg()
	if pkg == nil {
		return symbolgraph.ExternalSymbolRef{}, false, nil
	}
	if homeModule != "" && isSameModule(pkg.Path(), homeModule) {
		return symbolgraph.ExternalSymbolRef{}, false, nil
	}

	module := packagePathToModule[pkg.Path()]
	if module == "" {
		// No module info for this package (standard library, or a
		// GOPATH-mode dependency) — the package path is the best
		// identity available.
		module = pkg.Path()
	}

	sourceFile := ""
	if pos := obj.Pos(); pos.IsValid() {
		sourceFile = fset.Position(pos).Filename
	}

	return symbolgraph.ExternalSymbolRef{
		Ecosystem:     "go",
		Module:        module,
		Package:       pkg.Name(),
		QualifiedName: qualifiedName(obj),
		SourceFile:    sourceFile,
	}, true, nil
}

// isSameModule reports whether pkgPath belongs to module homeModule —
// an exact prefix match on path segments, not a naive string prefix (so
// "example.com/foobar" is not wrongly treated as part of module
// "example.com/foo").
func isSameModule(pkgPath, homeModule string) bool {
	if pkgPath == homeModule {
		return true
	}
	return strings.HasPrefix(pkgPath, homeModule+"/")
}

// qualifiedName renders obj's name, receiver-qualified for a method
// (e.g. "(*Tx).Bucket") the same way godoc/go doc already present it.
func qualifiedName(obj types.Object) string {
	fn, ok := obj.(*types.Func)
	if !ok {
		return obj.Name()
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return fn.Name()
	}
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		return fmt.Sprintf("(*%s).%s", typeName(ptr.Elem()), fn.Name())
	}
	return fmt.Sprintf("%s.%s", typeName(recv), fn.Name())
}

func typeName(t types.Type) string {
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return t.String()
}

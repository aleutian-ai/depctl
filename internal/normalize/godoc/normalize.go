package godoc

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strings"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize"
)

// Normalize parses the Go package directory at src.LocalPath and emits
// one KnowledgeObject for the package doc comment plus one per exported
// type, interface, function, method, and constant.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, src.LocalPath, nil, parser.ParseComments)
	if err != nil {
		// parser.ParseDir aborts on the first unparseable .go file in the
		// directory, even if the rest parse fine. Real repos legitimately
		// contain intentionally-invalid Go source (e.g. golang/tools'
		// cmd/fiximports/testdata fixtures, which exist specifically to
		// exercise error handling) — that's not evidence of a bug in this
		// normalizer, it's a directory with no valid documentable package,
		// same as the no-non-test-package case below. Skip it rather than
		// failing the whole generation over test fixtures.
		return nil, nil
	}

	astPkg, ok := singlePackage(pkgs)
	if !ok {
		// A directory containing only an external "foo_test" package (no
		// importable non-test package) has nothing to document — a
		// normal, common Go layout (integration-test-only dirs), not a
		// content error. Skip it silently, the same way normalize.NewRegistry.
		// Select does for a file type no normalizer recognizes, rather
		// than failing the whole generation (Build treats a real
		// normalizer error as fatal to every other object it already
		// staged).
		return nil, nil
	}

	files := make([]*ast.File, 0, len(astPkg.Files))
	for _, f := range astPkg.Files {
		files = append(files, f)
	}

	// Mode 0 (no doc.AllDecls) is what gives us exported-only filtering —
	// AllDecls actually means "include unexported package-level
	// declarations too", the opposite of what its name suggests at a
	// glance. Verified against the go/doc source (doc.go's Mode docs)
	// before relying on it here.
	docPkg, err := doc.NewFromFiles(fset, files, importPath(src))
	if err != nil {
		return nil, fmt.Errorf("godoc: build docs for %s: %w", src.LocalPath, err)
	}

	var objects []domain.KnowledgeObject

	if docPkg.Doc != "" {
		objects = append(objects, domain.KnowledgeObject{
			SourceID:    src.SourceID,
			SourceURI:   src.URI,
			ContentType: "package_doc",
			LogicalPath: src.LogicalPath,
			Title:       docPkg.Name,
			Version:     src.Version,
			Commit:      src.Commit,
			Content:     normalize.NormalizeLineEndings([]byte(docPkg.Doc)),
			Metadata:    map[string]string{"package": docPkg.Name},
		})
	}

	for _, fn := range docPkg.Funcs {
		objects = append(objects, symbolObject(src, fset, docPkg.Name, fn.Name, "", fn.Doc, fn.Decl))
	}
	for _, c := range docPkg.Consts {
		for _, name := range c.Names {
			objects = append(objects, symbolObject(src, fset, docPkg.Name, name, "", c.Doc, c.Decl))
		}
	}
	for _, t := range docPkg.Types {
		objects = append(objects, symbolObject(src, fset, docPkg.Name, t.Name, "", t.Doc, t.Decl))
		for _, m := range t.Methods {
			objects = append(objects, symbolObject(src, fset, docPkg.Name, m.Name, t.Name, m.Doc, m.Decl))
		}
		// go/doc groups a package-level function under the type it
		// returns (Type.Funcs) rather than the package-level Doc.Funcs
		// list — the extremely common Go constructor convention
		// (NewFoo(), Open(), Connect() returning *Client, etc.). Missing
		// this meant every such constructor function was silently never
		// indexed at all, regardless of how prominent it is in a real
		// dependency's API (found via VALID-004's live fixture, which
		// specifically used a Connect(...) (*Client, error) constructor).
		for _, fn := range t.Funcs {
			objects = append(objects, symbolObject(src, fset, docPkg.Name, fn.Name, "", fn.Doc, fn.Decl))
		}
	}

	return objects, nil
}

// singlePackage picks the package to document from parser.ParseDir's
// result — a directory can also yield an external "foo_test" package,
// which isn't the API surface being documented. A library directory can
// also hold a build-ignored `package main` generator (//go:build ignore),
// which ParseDir folds in as a second package: an importable package is
// preferred over "main", since a generator is not part of the library's
// API (choosing alphabetically documented the generator, and indexed
// nothing, for any library named after "m"). A directory with only a main
// package is a command and keeps it. ok is false if dir has no non-test
// package (e.g. an integration-test-only directory).
func singlePackage(pkgs map[string]*ast.Package) (pkg *ast.Package, ok bool) {
	var names []string
	for name := range pkgs {
		if !strings.HasSuffix(name, "_test") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if name != "main" {
			return pkgs[name], true
		}
	}
	if len(names) == 0 {
		return nil, false
	}
	return pkgs[names[0]], true
}

// maxSignatureBytes is the largest grouped declaration (a const/var/type
// block) whose whole text is used as one symbol's signature. go/doc
// reports a whole const block as one declaration, so every name in it
// would otherwise carry the entire block: a generated table of N names
// costs N*N text (2.4 GB live in one real dependency's build). Past this
// size a name's signature is just its own spec.
const maxSignatureBytes = 4 << 10

// symbolObject builds the KnowledgeObject for one exported symbol.
// receiver is set only for methods, naming the type they're associated
// with.
func symbolObject(src domain.SourceSnapshot, fset *token.FileSet, pkgName, name, receiver, docText string, decl ast.Node) domain.KnowledgeObject {
	metadata := map[string]string{
		"package": pkgName,
		"symbol":  name,
	}
	if sig := signature(fset, decl, name); sig != "" {
		metadata["signature"] = sig
	}
	if receiver != "" {
		metadata["receiver"] = receiver
	}

	return domain.KnowledgeObject{
		SourceID:    src.SourceID,
		SourceURI:   src.URI,
		ContentType: "symbol_doc",
		LogicalPath: src.LogicalPath,
		Title:       name,
		Version:     src.Version,
		Commit:      src.Commit,
		Content:     normalize.NormalizeLineEndings([]byte(docText)),
		Metadata:    metadata,
	}
}

// narrowLargeGroup reduces a grouped declaration bigger than
// maxSignatureBytes to only the spec declaring name, sized from its
// source span so the oversized block is never printed just to measure it.
func narrowLargeGroup(decl ast.Node, name string) ast.Node {
	gd, ok := decl.(*ast.GenDecl)
	if !ok || len(gd.Specs) < 2 || int(gd.End()-gd.Pos()) <= maxSignatureBytes {
		return decl
	}
	for _, spec := range gd.Specs {
		if specDeclares(spec, name) {
			single := *gd
			single.Specs = []ast.Spec{spec}
			single.Lparen, single.Rparen = token.NoPos, token.NoPos
			return &single
		}
	}
	return decl
}

func specDeclares(spec ast.Spec, name string) bool {
	switch sp := spec.(type) {
	case *ast.ValueSpec:
		for _, n := range sp.Names {
			if n.Name == name {
				return true
			}
		}
	case *ast.TypeSpec:
		return sp.Name.Name == name
	}
	return false
}

// signature renders decl's declaration syntax (signature, or full type
// definition for a type/const group) without its doc comment or —  for
// a func — its body, via go/printer.
func signature(fset *token.FileSet, decl ast.Node, name string) string {
	decl = narrowLargeGroup(decl, name)
	var toPrint ast.Node
	switch d := decl.(type) {
	case *ast.FuncDecl:
		clone := *d
		clone.Body = nil
		clone.Doc = nil
		toPrint = &clone
	case *ast.GenDecl:
		clone := *d
		clone.Doc = nil
		toPrint = &clone
	default:
		toPrint = decl
	}

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, toPrint); err != nil {
		return ""
	}
	return buf.String()
}

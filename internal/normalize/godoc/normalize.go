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
)

// Normalize parses the Go package directory at src.LocalPath and emits
// one KnowledgeObject for the package doc comment plus one per exported
// type, interface, function, method, and constant.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, src.LocalPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("godoc: parse %s: %w", src.LocalPath, err)
	}

	astPkg, err := singlePackage(pkgs, src.LocalPath)
	if err != nil {
		return nil, err
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
			Content:     []byte(docPkg.Doc),
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
	}

	return objects, nil
}

// singlePackage picks the non-"_test" package from parser.ParseDir's
// result — a directory can also yield an external "foo_test" package,
// which isn't the API surface being documented.
func singlePackage(pkgs map[string]*ast.Package, dir string) (*ast.Package, error) {
	var names []string
	for name := range pkgs {
		if !strings.HasSuffix(name, "_test") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("godoc: no non-test package found in %s", dir)
	}
	return pkgs[names[0]], nil
}

// symbolObject builds the KnowledgeObject for one exported symbol.
// receiver is set only for methods, naming the type they're associated
// with.
func symbolObject(src domain.SourceSnapshot, fset *token.FileSet, pkgName, name, receiver, docText string, decl ast.Node) domain.KnowledgeObject {
	metadata := map[string]string{
		"package": pkgName,
		"symbol":  name,
	}
	if sig := signature(fset, decl); sig != "" {
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
		Content:     []byte(docText),
		Metadata:    metadata,
	}
}

// signature renders decl's declaration syntax (signature, or full type
// definition for a type/const group) without its doc comment or —  for
// a func — its body, via go/printer.
func signature(fset *token.FileSet, decl ast.Node) string {
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

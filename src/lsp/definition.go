package lsp

import (
	"path/filepath"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/loader"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Definition answers a textDocument/definition request: the source location
// of whatever symbol resolves at pos, if any. Since Symbol.Decl is only
// meaningful relative to Symbol.Tree (see that field's own doc comment - a
// symbol may be declared in a different file of the same package, or a
// different package entirely, than the one being hovered), the returned
// Location always points at the symbol's own declaring file, not
// necessarily path.
//
// Points at the declaring *name* specifically (sym.DeclaringNameNode - see
// its own doc comment for why that differs from sym.Decl's full span),
// falling back to sym.Decl itself only for the one case it can't resolve
// further (SymReceiver).
func (w *Workspace) Definition(path string, pos protocol.Position) *protocol.Location {
	fa, n, ok := w.resolveNode(path, pos)
	if !ok {
		return nil
	}

	// An ImportDecl's own Info.Refs entry (see resolve.go's buildFileScope)
	// points its Decl/Tree straight back at the import statement itself -
	// there's no single "declaration site" for an imported package the way
	// there is for a func/struct/var, so following Symbol.Decl here would
	// just jump to the import line, not the package it names. Resolve the
	// target directory the same way loader.LoadProgram itself does (path
	// relative to the importing file's own directory - see LANGUAGE.md's
	// "Imports" section) and jump into its first file instead.
	if fa.Tree.Nodes[n].Kind == enums.NodeKinds.ImportDecl {
		return w.importTargetLocation(fa.Tree, n)
	}

	sym, ok := fa.Info.Refs[n]
	if !ok || sym == nil || sym.Tree == nil || sym.Decl == ast.InvalidNode {
		// No Ref recorded at all, or a predeclared symbol (print, int, ...)
		// with no real declaration site in any source file.
		return nil
	}

	target := sym.DeclaringNameNode(sym.Tree)
	if target == ast.InvalidNode {
		target = sym.Decl
	}
	return &protocol.Location{
		URI:   URIFromPath(sym.Tree.File.Name),
		Range: spanToRange(sym.Tree.File, sym.Tree.SpanOf(target)),
	}
}

// importTargetLocation resolves importDecl's own path text to a real
// on-disk package directory and returns a Location pointing at the start of
// that directory's first file (sorted by name, matching loader.Load's own
// deterministic ordering) - there's no more precise "declaration site"
// within a package to point at than that.
func (w *Workspace) importTargetLocation(tree *ast.Tree, importDecl ast.NodeIndex) *protocol.Location {
	importPath := tree.File.StringValue(tree.Nodes[importDecl].Tok)
	fileDir := filepath.Dir(tree.File.Name)
	targetDir := filepath.Clean(filepath.Join(fileDir, filepath.FromSlash(importPath)))

	files, err := loader.Load(w.fs, targetDir)
	if err != nil || len(files) == 0 {
		return nil
	}

	zero := protocol.Position{
		Line:      0,
		Character: 0,
	}
	return &protocol.Location{
		URI: URIFromPath(files[0].Name),
		Range: protocol.Range{
			Start: zero,
			End:   zero,
		},
	}
}

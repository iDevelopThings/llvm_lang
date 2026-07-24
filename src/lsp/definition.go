package lsp

import (
	"llvm_lang/src/ast"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Definition answers a textDocument/definition request: the source location
// of whatever symbol resolves at pos, if any. Since Symbol.Decl is only
// meaningful relative to Symbol.Tree (see that field's own doc comment - a
// symbol may be declared in a different file of the same package, or a
// different package entirely, than the one being hovered), the returned
// Location always points at the symbol's own declaring file, not
// necessarily path.
func (w *Workspace) Definition(path string, pos protocol.Position) *protocol.Location {
	fa, ok := w.Analysis(path)
	if !ok || fa.Info == nil {
		return nil
	}

	offset := positionToByteOffset(fa.Tree.File.Src, pos)
	n := fa.Tree.NodeAt(offset)
	if n == ast.InvalidNode {
		return nil
	}

	sym, ok := fa.Info.Refs[n]
	if !ok || sym == nil || sym.Tree == nil || sym.Decl == ast.InvalidNode {
		// No Ref recorded at all, or a predeclared symbol (print, int, ...)
		// with no real declaration site in any source file.
		return nil
	}

	declSpan := sym.Tree.SpanOf(sym.Decl)
	return &protocol.Location{
		URI: URIFromPath(sym.Tree.File.Name),
		Range: protocol.Range{
			Start: byteOffsetToPosition(sym.Tree.File, declSpan.Start),
			End:   byteOffsetToPosition(sym.Tree.File, declSpan.End),
		},
	}
}

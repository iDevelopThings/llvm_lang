package lsp

import (
	"llvm_lang/src/ast"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// References answers a textDocument/references request: every occurrence of
// whatever symbol resolves at pos, across every file this Workspace has
// analyzed as part of the *same* recompute as path's own current analysis
// (see FileAnalysis.Generation's own doc comment for why cross-generation
// Symbol pointers are never safely comparable) - optionally including the
// declaration's own occurrence, per includeDeclaration (LSP's own
// ReferenceContext.IncludeDeclaration), identified via
// sema.Symbol.DeclaringNameNode.
//
// Scoped to the current recompute's own file set, which - same limitation
// as Definition/diagnostics (see Workspace.OpenOrChange's own doc comment)
// - means a downstream package that imports this one but hasn't itself been
// opened/analyzed yet contributes no results, even if it references this
// symbol.
func (w *Workspace) References(path string, pos protocol.Position, includeDeclaration bool) []protocol.Location {
	fa, n, ok := w.resolveNode(path, pos)
	if !ok {
		return nil
	}
	sym, ok := fa.Info.Refs[n]
	if !ok || sym == nil {
		return nil
	}

	var declNameNode ast.NodeIndex
	if sym.Tree != nil {
		declNameNode = sym.DeclaringNameNode(sym.Tree)
	}

	var locs []protocol.Location
	for _, other := range w.analysisSnapshot() {
		if other.Info == nil || other.Generation != fa.Generation {
			continue
		}
		for refNode, refSym := range other.Info.Refs {
			if refSym != sym {
				continue
			}
			if !includeDeclaration && other.Tree == sym.Tree && refNode == declNameNode {
				continue
			}
			span := other.Tree.SpanOf(refNode)
			locs = append(locs, protocol.Location{
				URI: URIFromPath(other.Tree.File.Name),
				Range: protocol.Range{
					Start: byteOffsetToPosition(other.Tree.File, span.Start),
					End:   byteOffsetToPosition(other.Tree.File, span.End),
				},
			})
		}
	}
	return locs
}

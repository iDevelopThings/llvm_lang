package lsp

import (
	"llvm_lang/src/ast"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// DocumentHighlight answers a textDocument/documentHighlight request: every
// occurrence of whatever symbol resolves at pos, within path alone (unlike
// References, which searches the whole recompute's file set - a highlight
// is a same-document editor decoration, never a cross-file result) - each
// tagged Read or Write. Write covers both a later reassignment/inc-dec
// (ast.Tree.IsAssignmentTarget) and the symbol's own initial declaring
// occurrence (sym.DeclaringNameNode) - `total := 0` writes total's value
// just as much as a later `total = 1` does, even though DeclaringNameNode
// isn't itself an AssignStmt/IncDecStmt/MultiAssignStmt target.
func (w *Workspace) DocumentHighlight(path string, pos protocol.Position) []protocol.DocumentHighlight {
	fa, n, ok := w.resolveNode(path, pos)
	if !ok {
		return nil
	}
	sym, ok := fa.Info.Refs[n]
	if !ok || sym == nil {
		return nil
	}

	// sym.DeclaringNameNode(sym.Tree) is only comparable against this
	// file's own Refs keys when sym is actually declared in this same file
	// - a NodeIndex is meaningless compared across two different Trees
	// (see ast.NodeIndex's own doc comment). A symbol declared elsewhere
	// (an imported function, say) simply has no declaring occurrence to
	// match within path, which declNode's InvalidNode zero value already
	// expresses correctly (no real refNode ever equals it).
	var declNode ast.NodeIndex
	if sym.Tree == fa.Tree {
		declNode = sym.DeclaringNameNode(fa.Tree)
	}

	var out []protocol.DocumentHighlight
	for refNode, refSym := range fa.Info.Refs {
		if refSym != sym {
			continue
		}
		if fa.Tree.RootAncestor(refNode) != fa.Tree.Root {
			// A monomorphized-generic instantiation's own clone (see
			// ast.Tree.CloneSubtree) - same symbol, but not a real
			// occurrence in this file's own source text.
			continue
		}
		kind := protocol.DocumentHighlightKindRead
		if refNode == declNode || fa.Tree.IsAssignmentTarget(refNode) {
			kind = protocol.DocumentHighlightKindWrite
		}
		out = append(out, protocol.DocumentHighlight{
			Range: spanToRange(fa.Tree.File, fa.Tree.SpanOf(refNode)),
			Kind:  &kind,
		})
	}
	return out
}

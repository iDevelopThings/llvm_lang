package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

// nodeAt returns the innermost node in tree whose span contains pos, or
// ast.InvalidNode if pos falls outside the tree entirely - the "what is
// under the cursor" query hover/definition both need.
//
// TODO(lsp): move to src/ast (as a Tree method, e.g. Tree.NodeAt) once core
// packages are safe to touch again (see doc.go's scope-constraint note) -
// this is generic AST position navigation with no LSP-specific concern of
// its own, and belongs next to ast.Tree's existing Children/Parent/SpanOf
// accessors rather than living in a downstream consumer package.
func nodeAt(tree *ast.Tree, pos lexer.Pos) ast.NodeIndex {
	if tree == nil || tree.Root == ast.InvalidNode {
		return ast.InvalidNode
	}
	return nodeAtIn(tree, tree.Root, pos)
}

func nodeAtIn(tree *ast.Tree, n ast.NodeIndex, pos lexer.Pos) ast.NodeIndex {
	span := tree.SpanOf(n)
	if pos < span.Start || pos > span.End {
		return ast.InvalidNode
	}
	for _, c := range tree.Children(n) {
		if c == ast.InvalidNode {
			continue
		}
		if found := nodeAtIn(tree, c, pos); found != ast.InvalidNode {
			return found
		}
	}
	return n
}

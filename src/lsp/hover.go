package lsp

import (
	"fmt"
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Hover answers a textDocument/hover request: path's own resolved symbol
// (name/kind) and inferred type at pos, if any - nil when path has no
// analysis yet, analysis never reached type-checking (a parse error
// somewhere in the package), or pos doesn't land on a node with anything to
// show.
func (w *Workspace) Hover(path string, pos protocol.Position) *protocol.Hover {
	fa, n, ok := w.resolveNode(path, pos)
	if !ok {
		return nil
	}

	var lines []string
	var sym *sema.Symbol
	if s, ok := fa.Info.Refs[n]; ok && s != nil {
		sym = s
		lines = append(lines, fmt.Sprintf("**%s** `%s`", sym.Kind, sym.Name))
		if detail := symbolDetail(w, sym); detail != "" {
			lines = append(lines, fmt.Sprintf("`%s`", detail))
		}
	}
	if typ, ok := fa.Info.Types[n]; ok && !typ.IsInvalid() {
		lines = append(lines, fmt.Sprintf("type: `%s`", typ.String()))
	}
	if sym != nil && sym.Tree != nil && sym.Decl != ast.InvalidNode {
		if doc := sym.Tree.DocComment(sym.Decl); doc != "" {
			lines = append(lines, doc)
		}
	}
	if len(lines) == 0 {
		return nil
	}

	rng := spanToRange(fa.Tree.File, fa.Tree.SpanOf(n))
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.MarkupKindMarkdown,
			Value: strings.Join(lines, "\n\n"),
		},
		Range: &rng,
	}
}

package lsp

import (
	"fmt"
	"strings"

	"llvm_lang/src/ast"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Hover answers a textDocument/hover request: path's own resolved symbol
// (name/kind) and inferred type at pos, if any - nil when path has no
// analysis yet, analysis never reached type-checking (a parse error
// somewhere in the package), or pos doesn't land on a node with anything to
// show.
func (w *Workspace) Hover(path string, pos protocol.Position) *protocol.Hover {
	fa, ok := w.Analysis(path)
	if !ok || fa.Info == nil {
		return nil
	}

	offset := positionToByteOffset(fa.Tree.File.Src, pos)
	n := fa.Tree.NodeAt(offset)
	if n == ast.InvalidNode {
		return nil
	}

	var lines []string
	if sym, ok := fa.Info.Refs[n]; ok && sym != nil {
		lines = append(lines, fmt.Sprintf("**%s** `%s`", sym.Kind, sym.Name))
	}
	if typ, ok := fa.Info.Types[n]; ok && !typ.IsInvalid() {
		lines = append(lines, fmt.Sprintf("type: `%s`", typ.String()))
	}
	if len(lines) == 0 {
		return nil
	}

	span := fa.Tree.SpanOf(n)
	rng := protocol.Range{
		Start: byteOffsetToPosition(fa.Tree.File, span.Start),
		End:   byteOffsetToPosition(fa.Tree.File, span.End),
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.MarkupKindMarkdown,
			Value: strings.Join(lines, "\n\n"),
		},
		Range: &rng,
	}
}

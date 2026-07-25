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
		if detail := symbolDetail(w.infoForTree(sym.Tree), sym); detail != "" {
			// Fenced, not inline backticks: most markdown-hover clients
			// (this project's own LSP4IJ template included) only apply
			// per-token syntax highlighting inside a fenced block, never a
			// single-line inline code span. Tagged "go" rather than this
			// project's own "llx" language ID - no client bundles a
			// grammar for a hobby language's own ID, but this language's
			// syntax is close enough to Go's that a Go grammar renders it
			// reasonably, and Go highlighting is near-universally bundled.
			lines = append(lines, fenceGo(detail))
		}
		if sym.Kind == sema.SymStruct && sym.StructInfo != nil {
			if layout, ok := sema.StructLayoutOf(sym.StructInfo, w.resolveStructFields); ok {
				lines = append(lines, fenceGo(formatStructLayout(layout)))
			}
		}
	}
	if typ, ok := fa.Info.Types[n]; ok && !typ.IsInvalid() {
		lines = append(lines, "type:\n"+fenceGo(typ.String()))
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

// fenceGo wraps code in a "go"-tagged fenced markdown block - see this
// function's own call sites for why "go" specifically.
func fenceGo(code string) string {
	return "```go\n" + code + "\n```"
}

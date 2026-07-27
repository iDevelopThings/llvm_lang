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
		// One fenced line, not a bold "kind `name`" heading plus a
		// separately-fenced signature: the two used to render as visually
		// disconnected fragments ("func Insert" then, on its own paragraph,
		// "(v int) int") - folding them into a single declaration-shaped
		// line lets the client's own Go grammar highlight the whole thing
		// (keyword, name, param/field types) the way a real declaration
		// would read, not just the detail half.
		//
		// Fenced, not inline backticks: most markdown-hover clients (this
		// project's own LSP4IJ template included) only apply per-token
		// syntax highlighting inside a fenced block, never a single-line
		// inline code span. Tagged "go" rather than this project's own
		// "llx" language ID - no client bundles a grammar for a hobby
		// language's own ID, but this language's syntax is close enough to
		// Go's that a Go grammar renders it reasonably, and Go highlighting
		// is near-universally bundled.
		lines = append(lines, fenceGo(hoverHeader(w.infoForTree(sym.Tree), sym)))
		switch {
		case sym.Kind == sema.SymStruct && sym.StructInfo != nil:
			if layout, ok := sema.StructLayoutOf(sym.StructInfo, w.resolveStructFields); ok {
				lines = append(lines, fenceGo(formatStructLayout(layout)))
			}
		case sym.Kind == sema.SymField && sym.StructInfo != nil:
			lines = append(lines, fmt.Sprintf("in struct `%s`", sym.StructInfo.Symbol.Name))
			if layout, ok := sema.StructLayoutOf(sym.StructInfo, w.resolveStructFields); ok {
				if fieldText, ok := formatFieldLayout(layout, sym.Name); ok {
					lines = append(lines, fenceGo(fieldText))
				}
			}
		}
	}
	typeNode := n
	if param := fa.Tree.ParamOf(n); param != ast.InvalidNode {
		typeNode = param
	}
	if typ, ok := fa.Info.Types[typeNode]; ok && !typ.IsInvalid() {
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

// hoverHeader renders sym's own kind keyword + name, with symbolDetail's
// signature/field-list (when it has one) folded onto the same line the way
// this language's own declaration syntax would write it - a func's params
// directly after its name ("func Insert(v int) int"), a struct's fields
// after a space ("struct Point { x int, y int }") - rather than
// symbolDetail's own bare fragment, which completion.go's own CompletionItem
// use of symbolDetail needs unprefixed and unchanged.
func hoverHeader(info *sema.Info, sym *sema.Symbol) string {
	detail := symbolDetail(info, sym)
	switch {
	case sym.Kind == sema.SymConstructor || sym.Kind == sema.SymDestructor:
		// Name already reads "<Struct>.constructor(<arity>)"/
		// "<Struct>.destructor" (see resolve.go's declareConstructor/
		// declareDestructor) - prefixing the kind again would just repeat it
		// ("constructor Point.constructor(2)").
		return sym.Name
	case detail == "":
		return fmt.Sprintf("%s %s", sym.Kind, sym.Name)
	case sym.Kind == sema.SymFunc:
		return fmt.Sprintf("func %s%s", sym.Name, detail)
	case sym.Kind == sema.SymStruct:
		return fmt.Sprintf("struct %s %s", sym.Name, detail)
	default:
		return fmt.Sprintf("%s %s %s", sym.Kind, sym.Name, detail)
	}
}

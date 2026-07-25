package lsp

import (
	"fmt"
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/sema"
)

// resolveStructFields answers sema.ResolveStructFields for the LSP: si's own
// fields' names/Types, in declaration order, read from its declaring file's
// own AST + Info via infoForTree - which may not be the file the hover
// request came from (see symbolDetail's own doc comment for the identical
// cross-file concern already handled for signature rendering).
func (w *Workspace) resolveStructFields(si *sema.StructInfo) ([]sema.FieldSpec, bool) {
	if si == nil || si.Symbol == nil || si.Symbol.Tree == nil || si.Symbol.Decl == ast.InvalidNode {
		return nil, false
	}
	tree := si.Symbol.Tree
	info := w.infoForTree(tree)
	if info == nil {
		return nil, false
	}

	var fields []sema.FieldSpec
	for nameNode, typeNode := range tree.StructFieldNodes(si.Symbol.Decl) {
		typ, ok := info.Types[typeNode]
		if !ok || typ.IsInvalid() {
			return nil, false
		}
		fields = append(fields, sema.FieldSpec{Name: tree.Text(nameNode), Type: typ})
	}
	return fields, true
}

// formatStructLayout renders layout as a CLion-style "Type Info" summary: an
// offset/size line per field in declaration order, an explicit line for any
// gap alignment spends between/after fields, and the struct's own total
// size/align/padding underneath.
func formatStructLayout(layout sema.StructLayout) string {
	var b strings.Builder
	next := uint64(0)
	for _, f := range layout.Fields {
		if f.Offset > next {
			fmt.Fprintf(&b, "+%-4d %s (%d byte padding)\n", next, strings.Repeat("~", 8), f.Offset-next)
		}
		fmt.Fprintf(&b, "+%-4d %-15s %-15s %d bytes\n", f.Offset, f.Name, f.Type, f.Size)
		next = f.Offset + f.Size
	}
	if layout.Size > next {
		fmt.Fprintf(&b, "+%-4d %s (%d byte padding)\n", next, strings.Repeat("~", 8), layout.Size-next)
	}
	fmt.Fprintf(&b, "\nsize = %d, align = %d, padding = %d", layout.Size, layout.Align, layout.Padding)
	return b.String()
}

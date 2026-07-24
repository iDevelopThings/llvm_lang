package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// DocumentSymbols answers a textDocument/documentSymbol request: a
// hierarchical outline of path's own top-level declarations - var/func/
// struct/enum/extern func (import declarations are omitted - they name a
// package binding, not something meaningful to jump to within an outline).
// nil when path has no analysis yet.
//
// A struct's own fields/constructors/destructor and an enum's own variants/
// destructor nest as children, since they're already ast.Tree children of
// the struct/enum declaration itself - free to include with no extra
// cross-referencing. A struct's *methods*, by contrast, are deliberately
// left as flat top-level entries rather than nested under their receiver
// struct: they're separate top-level FuncDecls elsewhere in the file (see
// LANGUAGE.md's "Structs" section - methods are declared apart from the
// struct itself, not nested syntactically), and nesting them here would
// need correlating each FuncDecl's own receiver name against every
// StructDecl - a real, separate feature, not a natural consequence of the
// tree shape the way fields/constructors already are.
func (w *Workspace) DocumentSymbols(path string) []protocol.DocumentSymbol {
	fa, ok := w.Analysis(path)
	if !ok || fa.Tree == nil {
		return nil
	}

	var out []protocol.DocumentSymbol
	for _, decl := range fa.Tree.Children(fa.Tree.Root) {
		if sym, ok := declSymbol(fa.Tree, decl); ok {
			out = append(out, sym)
		}
	}
	return out
}

// declSymbol builds decl's own outline entry, or reports ok=false for a
// declaration kind with no outline representation (ImportDecl).
func declSymbol(tree *ast.Tree, decl ast.NodeIndex) (sym protocol.DocumentSymbol, ok bool) {
	switch tree.Nodes[decl].Kind {
	case enums.NodeKinds.VarDecl:
		return namedSymbol(tree, decl, tree.Child(decl, 0), protocol.SymbolKindVariable, nil), true
	case enums.NodeKinds.FuncDecl:
		kind := protocol.SymbolKindFunction
		if tree.FuncReceiver(decl) != ast.InvalidNode {
			kind = protocol.SymbolKindMethod
		}
		return namedSymbol(tree, decl, tree.FuncName(decl), kind, nil), true
	case enums.NodeKinds.ExternFuncDecl:
		return namedSymbol(tree, decl, tree.ExternFuncName(decl), protocol.SymbolKindFunction, nil), true
	case enums.NodeKinds.StructDecl:
		return structSymbol(tree, decl), true
	case enums.NodeKinds.EnumDecl:
		return enumSymbol(tree, decl), true
	default:
		return protocol.DocumentSymbol{}, false
	}
}

func structSymbol(tree *ast.Tree, decl ast.NodeIndex) protocol.DocumentSymbol {
	var children []protocol.DocumentSymbol
	for _, field := range tree.StructFields(decl) {
		children = append(children, namedSymbol(tree, field, tree.Child(field, 0), protocol.SymbolKindField, nil))
	}
	for ctor := range tree.StructConstructors(decl) {
		children = append(children, keywordSymbol(tree, ctor, protocol.SymbolKindConstructor))
	}
	for dtor := range tree.StructDestructors(decl) {
		// LSP's SymbolKind has no dedicated "destructor" - Method is the
		// closer fit of the two special-method kinds it does have.
		children = append(children, keywordSymbol(tree, dtor, protocol.SymbolKindMethod))
	}
	return namedSymbol(tree, decl, tree.Child(decl, 0), protocol.SymbolKindStruct, children)
}

func enumSymbol(tree *ast.Tree, decl ast.NodeIndex) protocol.DocumentSymbol {
	var children []protocol.DocumentSymbol
	for _, variant := range tree.EnumVariants(decl) {
		// An EnumVariant's own Tok already IS its name (see ast.Node's own
		// doc comment) - keywordSymbol's shape (name from the node's own
		// Tok, selection range narrowed to just that Tok) is exactly right
		// here too, not just for a constructor/destructor's keyword.
		children = append(children, keywordSymbol(tree, variant, protocol.SymbolKindEnumMember))
	}
	for dtor := range tree.EnumDestructors(decl) {
		children = append(children, keywordSymbol(tree, dtor, protocol.SymbolKindMethod))
	}
	return namedSymbol(tree, decl, tree.Child(decl, 0), protocol.SymbolKindEnum, children)
}

// namedSymbol builds an outline entry for decl, whose own name is a
// separate child node (nameNode) - Range covers decl's whole span,
// SelectionRange only nameNode's (the identifier a client should actually
// reveal/select when this entry is picked).
func namedSymbol(tree *ast.Tree, decl, nameNode ast.NodeIndex, kind protocol.SymbolKind, children []protocol.DocumentSymbol) protocol.DocumentSymbol {
	return protocol.DocumentSymbol{
		Name:           tree.Text(nameNode),
		Kind:           kind,
		Range:          spanToRange(tree.File, tree.SpanOf(decl)),
		SelectionRange: spanToRange(tree.File, tree.SpanOf(nameNode)),
		Children:       children,
	}
}

// keywordSymbol builds an outline entry for n, whose own Tok IS its name
// (a ConstructorDecl/DestructorDecl's leading keyword, or an EnumVariant's
// own name token - see ast.Node's own doc comment) - Range covers n's whole
// span, SelectionRange only n's own Tok.
func keywordSymbol(tree *ast.Tree, n ast.NodeIndex, kind protocol.SymbolKind) protocol.DocumentSymbol {
	tok := tree.Nodes[n].Tok
	return protocol.DocumentSymbol{
		Name:           tree.Text(n),
		Kind:           kind,
		Range:          spanToRange(tree.File, tree.SpanOf(n)),
		SelectionRange: spanToRange(tree.File, ast.Span{Start: tok.Start, End: tok.End}),
	}
}

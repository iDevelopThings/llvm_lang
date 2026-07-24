package ast

import "llvm_lang/src/enums"

// SymbolOutlineKind classifies a DeclSymbol - a generic "kind of named
// declaration" concept any consumer walking a Tree's own top-level
// structure might want (an LSP server's outline view, a docs generator, a
// future "list symbols" CLI) - deliberately its own enum, not whatever
// wire-format kind one specific consumer happens to need (see
// src/lsp/documentsymbol.go for how it maps onto LSP's own SymbolKind).
type SymbolOutlineKind int

const (
	SymbolOutlineVariable SymbolOutlineKind = iota
	SymbolOutlineFunction
	SymbolOutlineMethod
	SymbolOutlineStruct
	SymbolOutlineEnum
	SymbolOutlineField
	SymbolOutlineConstructor
	SymbolOutlineDestructor
	SymbolOutlineEnumMember
)

// DeclSymbol is one declaration's own outline entry: its display name,
// kind, full extent (Span - the whole declaration, e.g. a FuncDecl's own
// "func ... { ... }") and precise "name only" extent (NameSpan - just the
// identifier, e.g. "translate" within a method's own signature - contained by
// Span, per DocumentSymbol's own LSP doc comment), plus any nested children
// (a struct's fields/constructors/destructor, an enum's variants/
// destructor).
type DeclSymbol struct {
	Name     string
	Kind     SymbolOutlineKind
	Span     Span
	NameSpan Span
	Children []DeclSymbol
}

// DeclSymbols returns t's own top-level declarations (var/func/struct/enum/
// extern func) as a hierarchical outline - an ImportDecl contributes
// nothing (it names a package binding, not something meaningful to surface
// in an outline).
//
// A struct's own fields/constructors/destructor and an enum's own variants/
// destructor nest as children, since they're already t's own children of
// the struct/enum declaration itself. A struct's *methods*, by contrast,
// are deliberately left as flat top-level entries rather than nested under
// their receiver struct: they're separate top-level FuncDecls elsewhere in
// the tree (see LANGUAGE.md's "Structs" section - methods are declared
// apart from the struct itself, not nested syntactically), and nesting them
// here would need correlating each FuncDecl's own receiver name against
// every StructDecl - a real, separate feature, not a natural consequence of
// the tree shape the way fields/constructors already are.
func (t *Tree) DeclSymbols() []DeclSymbol {
	var out []DeclSymbol
	for _, decl := range t.Children(t.Root) {
		if sym, ok := t.declSymbol(decl); ok {
			out = append(out, sym)
		}
	}
	return out
}

// declSymbol builds decl's own outline entry, or reports ok=false for a
// declaration kind with no outline representation (ImportDecl).
func (t *Tree) declSymbol(decl NodeIndex) (sym DeclSymbol, ok bool) {
	switch t.Nodes[decl].Kind {
	case enums.NodeKinds.VarDecl:
		return t.namedSymbol(decl, t.Child(decl, 0), SymbolOutlineVariable, nil), true
	case enums.NodeKinds.FuncDecl:
		kind := SymbolOutlineFunction
		if t.FuncReceiver(decl) != InvalidNode {
			kind = SymbolOutlineMethod
		}
		return t.namedSymbol(decl, t.FuncName(decl), kind, nil), true
	case enums.NodeKinds.ExternFuncDecl:
		return t.namedSymbol(decl, t.ExternFuncName(decl), SymbolOutlineFunction, nil), true
	case enums.NodeKinds.StructDecl:
		return t.structSymbol(decl), true
	case enums.NodeKinds.EnumDecl:
		return t.enumSymbol(decl), true
	default:
		return DeclSymbol{}, false
	}
}

func (t *Tree) structSymbol(decl NodeIndex) DeclSymbol {
	var children []DeclSymbol
	for _, field := range t.StructFields(decl) {
		children = append(children, t.namedSymbol(field, t.Child(field, 0), SymbolOutlineField, nil))
	}
	for ctor := range t.StructConstructors(decl) {
		children = append(children, t.keywordSymbol(ctor, SymbolOutlineConstructor))
	}
	for dtor := range t.StructDestructors(decl) {
		children = append(children, t.keywordSymbol(dtor, SymbolOutlineDestructor))
	}
	return t.namedSymbol(decl, t.Child(decl, 0), SymbolOutlineStruct, children)
}

func (t *Tree) enumSymbol(decl NodeIndex) DeclSymbol {
	var children []DeclSymbol
	for _, variant := range t.EnumVariants(decl) {
		// An EnumVariant's own Tok already IS its name (see Node's own doc
		// comment) - keywordSymbol's shape (name from the node's own Tok,
		// name span narrowed to just that Tok) is exactly right here too,
		// not just for a constructor/destructor's keyword.
		children = append(children, t.keywordSymbol(variant, SymbolOutlineEnumMember))
	}
	for dtor := range t.EnumDestructors(decl) {
		children = append(children, t.keywordSymbol(dtor, SymbolOutlineDestructor))
	}
	return t.namedSymbol(decl, t.Child(decl, 0), SymbolOutlineEnum, children)
}

// namedSymbol builds an outline entry for decl, whose own name is a
// separate child node (nameNode).
func (t *Tree) namedSymbol(decl, nameNode NodeIndex, kind SymbolOutlineKind, children []DeclSymbol) DeclSymbol {
	return DeclSymbol{
		Name:     t.Text(nameNode),
		Kind:     kind,
		Span:     t.SpanOf(decl),
		NameSpan: t.SpanOf(nameNode),
		Children: children,
	}
}

// keywordSymbol builds an outline entry for n, whose own Tok IS its name (a
// ConstructorDecl/DestructorDecl's leading keyword, or an EnumVariant's own
// name token - see Node's own doc comment).
func (t *Tree) keywordSymbol(n NodeIndex, kind SymbolOutlineKind) DeclSymbol {
	tok := t.Nodes[n].Tok
	return DeclSymbol{
		Name:     t.Text(n),
		Kind:     kind,
		Span:     t.SpanOf(n),
		NameSpan: Span{Start: tok.Start, End: tok.End},
	}
}

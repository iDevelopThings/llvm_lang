package ast

import (
	"iter"

	"llvm_lang/src/enums"
)

// TopLevelDeclsOfKind yields every one of tree's top-level declarations
// (tree.Children(tree.Root)) whose Kind is exactly kind, in declaration
// order - the "walk every StructDecl/ImportDecl/VarDecl/FuncDecl at file
// scope, filtering by kind" idiom that used to be hand-rolled identically
// (a bare `for _, d := range tree.Children(tree.Root) { if
// tree.Nodes[d].Kind == X { ... } }` loop) across sema/resolve.go,
// sema/typecheck.go, and codegen/codegen.go - see AGENTS.md's Architecture
// section: this belongs here, once, not duplicated at every call site.
func (t *Tree) TopLevelDeclsOfKind(kind enums.NodeKind) iter.Seq[NodeIndex] {
	return func(yield func(NodeIndex) bool) {
		for _, d := range t.Children(t.Root) {
			if t.Nodes[d].Kind != kind {
				continue
			}
			if !yield(d) {
				return
			}
		}
	}
}

// StructFields returns decl's (a StructDecl's) Field children, skipping its
// leading name child and filtering out any interspersed ConstructorDecl
// children (see Node's own StructDecl doc comment for the
// [name, member0, member1, ...] shape this indexes into, and
// LANGUAGE.md's "Constructors" section for why a constructor can appear
// there too now). A plain slice, not iter.Seq: every real caller needs
// indexed/random access (a positional composite literal's i-th field, or a
// field-count comparison against len(elems)), not just a single forward
// pass - see AGENTS.md's Standards section for when a plain slice is the
// right call over an iterator.
func (t *Tree) StructFields(decl NodeIndex) []NodeIndex {
	children := t.Children(decl)[1:]
	fields := make([]NodeIndex, 0, len(children))
	for _, c := range children {
		if t.Nodes[c].Kind == enums.NodeKinds.Field {
			fields = append(fields, c)
		}
	}
	return fields
}

// StructConstructors yields decl's (a StructDecl's) ConstructorDecl children,
// in declaration order - the constructor-kind counterpart to StructFields
// (see LANGUAGE.md's "Constructors" section). iter.Seq, not a plain slice:
// every real caller (cataloging constructors during resolve, declaring/
// generating each one's LLVM signature/body during codegen) is a single
// forward pass, never indexed access - see AGENTS.md's Standards section.
func (t *Tree) StructConstructors(decl NodeIndex) iter.Seq[NodeIndex] {
	return func(yield func(NodeIndex) bool) {
		for _, c := range t.Children(decl)[1:] {
			if t.Nodes[c].Kind != enums.NodeKinds.ConstructorDecl {
				continue
			}
			if !yield(c) {
				return
			}
		}
	}
}

// ConstructorParamList returns ctor's (a ConstructorDecl's) ParamList child -
// see Node's own ConstructorDecl doc comment for the [paramList, body] shape
// these two accessors index into.
func (t *Tree) ConstructorParamList(ctor NodeIndex) NodeIndex {
	return t.Child(ctor, 0)
}

// ConstructorBody returns ctor's (a ConstructorDecl's) body child.
func (t *Tree) ConstructorBody(ctor NodeIndex) NodeIndex {
	return t.Child(ctor, 1)
}

// FuncLitParamList returns lit's (a FuncLit's) ParamList child - see Node's
// own FuncLit doc comment for the [paramList, returnType, body] shape these
// three accessors index into.
func (t *Tree) FuncLitParamList(lit NodeIndex) NodeIndex {
	return t.Child(lit, 0)
}

// FuncLitReturnType returns lit's (a FuncLit's) return-type child -
// InvalidNode when the literal declares no return type.
func (t *Tree) FuncLitReturnType(lit NodeIndex) NodeIndex {
	return t.Child(lit, 1)
}

// FuncLitBody returns lit's (a FuncLit's) body child.
func (t *Tree) FuncLitBody(lit NodeIndex) NodeIndex {
	return t.Child(lit, 2)
}

// CompositeLitElems splits n's (a CompositeLit's) children into its leading
// type-expr child and its remaining elements - see Node's own CompositeLit
// doc comment for the [typeExpr, elem0, elem1, ...] shape. elems is a plain
// slice, not iter.Seq, for the same "every real caller needs indexed access"
// reason StructFields is.
func (t *Tree) CompositeLitElems(n NodeIndex) (typeExpr NodeIndex, elems []NodeIndex) {
	children := t.Children(n)
	return children[0], children[1:]
}

// IsKeyedElement reports whether n - one element of a CompositeLit - is a
// keyed element (`field: value`, a KeyValueExpr node) rather than a
// positional one (a bare value expression) - see Node's own CompositeLit doc
// comment.
func (t *Tree) IsKeyedElement(n NodeIndex) bool {
	return t.Nodes[n].Kind == enums.NodeKinds.KeyValueExpr
}

// FuncReceiver returns decl's (a FuncDecl's) receiver child - InvalidNode
// for a free function - see Node's own FuncDecl doc comment for the
// [receiver, name, paramList, returnType, body] shape these five accessors
// index into, replacing a magic-index Child(decl, 0..4) call at every site
// that reads one part of a function declaration.
func (t *Tree) FuncReceiver(decl NodeIndex) NodeIndex {
	return t.Child(decl, 0)
}

// FuncName returns decl's (a FuncDecl's) name child.
func (t *Tree) FuncName(decl NodeIndex) NodeIndex {
	return t.Child(decl, 1)
}

// FuncParamList returns decl's (a FuncDecl's) ParamList child.
func (t *Tree) FuncParamList(decl NodeIndex) NodeIndex {
	return t.Child(decl, 2)
}

// FuncReturnType returns decl's (a FuncDecl's) return-type child -
// InvalidNode when the function declares no return type.
func (t *Tree) FuncReturnType(decl NodeIndex) NodeIndex {
	return t.Child(decl, 3)
}

// FuncBody returns decl's (a FuncDecl's) body child.
func (t *Tree) FuncBody(decl NodeIndex) NodeIndex {
	return t.Child(decl, 4)
}

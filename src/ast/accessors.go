package ast

import (
	"iter"

	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
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

// StructDestructors yields decl's (a StructDecl's) DestructorDecl children,
// in declaration order - the destructor-kind counterpart to
// StructConstructors (see LANGUAGE.md's "Destructors" section). A struct is
// meant to declare at most one - a second is a compile-time error (see
// sema.declareDestructor) - but this still yields every one found rather than
// just the first, so a duplicate is still visited (and its own body still
// type-checked/lowered) exactly like a duplicate-arity constructor is, not
// silently dropped.
func (t *Tree) StructDestructors(decl NodeIndex) iter.Seq[NodeIndex] {
	return func(yield func(NodeIndex) bool) {
		for _, c := range t.Children(decl)[1:] {
			if t.Nodes[c].Kind != enums.NodeKinds.DestructorDecl {
				continue
			}
			if !yield(c) {
				return
			}
		}
	}
}

// DestructorParamList returns dtor's (a DestructorDecl's) ParamList child -
// see Node's own DestructorDecl doc comment for the [paramList, body] shape
// these two accessors index into. Always semantically empty (sema rejects a
// non-empty one), but still a real ParamList node, exactly like
// ConstructorParamList's own shape.
func (t *Tree) DestructorParamList(dtor NodeIndex) NodeIndex {
	return t.Child(dtor, 0)
}

// DestructorBody returns dtor's (a DestructorDecl's) body child.
func (t *Tree) DestructorBody(dtor NodeIndex) NodeIndex {
	return t.Child(dtor, 1)
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

// ExternFuncName returns decl's (an ExternFuncDecl's) name child - see
// Node's own ExternFuncDecl doc comment for the [name, paramList, returnType]
// shape these three accessors index into. A deliberately separate, parallel
// set of accessors from FuncName/FuncParamList/FuncReturnType above, rather
// than reusing them against ExternFuncDecl's own 3-child layout - the two
// node kinds' children start at different positions (FuncDecl reserves slot 0
// for its receiver; ExternFuncDecl has none), so indexing an ExternFuncDecl
// through FuncDecl's own accessors would silently read the wrong child.
func (t *Tree) ExternFuncName(decl NodeIndex) NodeIndex {
	return t.Child(decl, 0)
}

// ExternFuncParamList returns decl's (an ExternFuncDecl's) ParamList child.
func (t *Tree) ExternFuncParamList(decl NodeIndex) NodeIndex {
	return t.Child(decl, 1)
}

// ExternFuncReturnType returns decl's (an ExternFuncDecl's) return-type
// child - InvalidNode when the declaration names no return type.
func (t *Tree) ExternFuncReturnType(decl NodeIndex) NodeIndex {
	return t.Child(decl, 2)
}

// MultiShortVarDeclNames returns decl's (a MultiShortVarDecl's) leading Ident
// name children - every child except the last (see Node's own
// MultiShortVarDecl doc comment for the [name0, ..., nameN, call] shape these
// two accessors index into). A plain slice, not iter.Seq: every real caller
// (declaring each name, storing each one's own destructured field) needs
// positional/indexed access paired against the call's own component types,
// not just a single forward pass.
func (t *Tree) MultiShortVarDeclNames(decl NodeIndex) []NodeIndex {
	children := t.Children(decl)
	return children[:len(children)-1]
}

// MultiShortVarDeclValue returns decl's (a MultiShortVarDecl's) trailing
// call-expression child - the sole right-hand side being destructured.
func (t *Tree) MultiShortVarDeclValue(decl NodeIndex) NodeIndex {
	children := t.Children(decl)
	return children[len(children)-1]
}

// EnumVariants returns decl's (an EnumDecl's) EnumVariant children, skipping
// its leading name child and filtering out its own (at most one)
// DestructorDecl child - the enum-kind counterpart to StructFields. A plain
// slice, not iter.Seq: real callers (cataloging every variant's discriminant
// index during resolve, checking exhaustiveness against the full variant
// set during Check) need the full set, indexed by declaration order, not
// just a single forward pass.
func (t *Tree) EnumVariants(decl NodeIndex) []NodeIndex {
	children := t.Children(decl)[1:]
	variants := make([]NodeIndex, 0, len(children))
	for _, c := range children {
		if t.Nodes[c].Kind == enums.NodeKinds.EnumVariant {
			variants = append(variants, c)
		}
	}
	return variants
}

// EnumDestructors yields decl's (an EnumDecl's) DestructorDecl children, in
// declaration order - the enum-kind counterpart to StructDestructors (see
// LANGUAGE.md's "Enums"/"Destructors" sections: an enum's destructor fires
// once, regardless of which variant is actually active, exactly like a
// struct's). An enum is meant to declare at most one, same "duplicate is
// still visited, not silently dropped" reasoning StructDestructors already
// documents.
func (t *Tree) EnumDestructors(decl NodeIndex) iter.Seq[NodeIndex] {
	return func(yield func(NodeIndex) bool) {
		for _, c := range t.Children(decl)[1:] {
			if t.Nodes[c].Kind != enums.NodeKinds.DestructorDecl {
				continue
			}
			if !yield(c) {
				return
			}
		}
	}
}

// EnumVariantKind classifies an EnumVariant node's own shape - unit, tuple,
// or struct-style (see ast.Node's own EnumVariant doc comment for exactly
// what distinguishes each). A small, package-local result type rather than a
// generated enum: nothing here needs a String()/Parse()/iteration helper
// beyond the three bare constants themselves (see AGENTS.md's enum_codegen
// criterion) - every caller already has its own reason to name the kind in
// a diagnostic, so there's nothing generic to hand-maintain in parallel.
type EnumVariantKind int

const (
	EnumVariantUnit EnumVariantKind = iota
	EnumVariantTuple
	EnumVariantStruct
)

// ClassifyEnumVariant reports variant's (an EnumVariant node's) own kind,
// purely from its children's shape - no children at all means a unit
// variant; children present but the first one isn't a Field node means a
// tuple variant (every child is a bare type-position node); a first child
// that IS a Field node means a struct variant (every child is a
// [name, type] pair) - a type-position node is never itself Kind == Field,
// so this is unambiguous.
func (t *Tree) ClassifyEnumVariant(variant NodeIndex) EnumVariantKind {
	children := t.Children(variant)
	if len(children) == 0 {
		return EnumVariantUnit
	}
	if t.Nodes[children[0]].Kind == enums.NodeKinds.Field {
		return EnumVariantStruct
	}
	return EnumVariantTuple
}

// MatchSubject returns n's (a MatchStmt's) subject child - the value being
// matched (see ast.Node's own MatchStmt doc comment for the
// [subject, arm0, arm1, ...] shape).
func (t *Tree) MatchSubject(n NodeIndex) NodeIndex {
	return t.Child(n, 0)
}

// MatchArms returns n's (a MatchStmt's) MatchArm children, in source order -
// every child except the leading subject. A plain slice, not iter.Seq: real
// callers (exhaustiveness checking, codegen's own switch-arm construction)
// need the full set together, not just a single forward pass.
func (t *Tree) MatchArms(n NodeIndex) []NodeIndex {
	return t.Children(n)[1:]
}

// MatchArmPatterns returns arm's (a MatchArm's) pattern children, in source
// order - every child except the trailing body (see ast.Node's own MatchArm
// doc comment for the [pattern0, ..., patternN, body] shape). At least one
// pattern is always present (parseMatchArm requires it) - a Go-`case a, b,
// c:`-style comma-separated list, generalized from the enum-match feature's
// original fixed single-pattern shape to also support a value-match arm's
// multi-value-per-arm form (see LANGUAGE.md's "match" section). A plain
// slice, not iter.Seq: every real caller (resolve/check/codegen) needs the
// full set together - to loop over every pattern, to count them for the
// enum-match "exactly one pattern" restriction, or to build a value-match
// arm's own short-circuit comparison chain - never just a single forward
// pass in isolation.
func (t *Tree) MatchArmPatterns(arm NodeIndex) []NodeIndex {
	children := t.Children(arm)
	return children[:len(children)-1]
}

// MatchArmPattern returns arm's (a MatchArm's) first pattern child - a
// convenience for callers that only ever need to look at a single pattern:
// the enum-match path (sema.checkEnumMatchStmt enforces exactly one pattern
// per enum-match arm - see LANGUAGE.md's "match" section - so its own first
// pattern is always the *only* pattern there). NOT a safe stand-in for
// MatchArmPatterns in a context that must consider every pattern an arm may
// have - a value-match arm can legitimately carry more than one.
func (t *Tree) MatchArmPattern(arm NodeIndex) NodeIndex {
	return t.MatchArmPatterns(arm)[0]
}

// MatchArmBody returns arm's (a MatchArm's) trailing body child - always the
// last child, regardless of how many leading patterns precede it.
func (t *Tree) MatchArmBody(arm NodeIndex) NodeIndex {
	children := t.Children(arm)
	return children[len(children)-1]
}

// IsWildcardMatchArm reports whether arm (a MatchArm) is the bare wildcard
// `_` arm: exactly one pattern, an Ident whose text is exactly "_". This is
// deliberately more than a bare `Kind == Ident` check (which every call site
// used before value-match patterns existed, back when an Ident pattern could
// only ever legally be "_" - see resolve.go's old resolvePattern): a plain
// identifier is now also a legal *value* pattern (referencing a variable/
// constant as a case value - see LANGUAGE.md's "match" section), so "is this
// arm the wildcard" and "is this arm's first pattern an Ident" are no longer
// the same question, and every caller that needs the former (exhaustiveness/
// coverage checks, codegen's own switch-default/final-fallback selection)
// must ask it precisely this way rather than approximating it.
func (t *Tree) IsWildcardMatchArm(arm NodeIndex) bool {
	patterns := t.MatchArmPatterns(arm)
	return len(patterns) == 1 && t.Nodes[patterns[0]].Kind == enums.NodeKinds.Ident && t.Text(patterns[0]) == "_"
}

// MultiAssignStmtTargets returns n's (a MultiAssignStmt's) leading lvalue
// target children - every child except the last (see Node's own
// MultiAssignStmt doc comment for the [target0, ..., targetN, call] shape
// these two accessors index into) - the assignment-form counterpart to
// MultiShortVarDeclNames, same reasoning.
func (t *Tree) MultiAssignStmtTargets(n NodeIndex) []NodeIndex {
	children := t.Children(n)
	return children[:len(children)-1]
}

// MultiAssignStmtValue returns n's (a MultiAssignStmt's) trailing
// call-expression child - the sole right-hand side being destructured.
func (t *Tree) MultiAssignStmtValue(n NodeIndex) NodeIndex {
	children := t.Children(n)
	return children[len(children)-1]
}

// NodeAt returns the innermost node whose span contains pos, or InvalidNode
// if pos falls outside the tree entirely - the "what is under the cursor"
// query a tool like an LSP needs (hover, go-to-definition). Descends only
// into children whose own span actually contains pos, skipping every
// sibling subtree that doesn't - O(depth), not O(tree size): span
// containment forms a strict nesting (a child's span is always inside its
// parent's), so a node whose span excludes pos can never have a descendant
// that includes it either.
func (t *Tree) NodeAt(pos lexer.Pos) NodeIndex {
	if t.Root == InvalidNode {
		return InvalidNode
	}
	return t.nodeAtIn(t.Root, pos)
}

func (t *Tree) nodeAtIn(n NodeIndex, pos lexer.Pos) NodeIndex {
	span := t.SpanOf(n)
	if pos < span.Start || pos > span.End {
		return InvalidNode
	}
	for _, c := range t.Children(n) {
		if c == InvalidNode {
			continue
		}
		if found := t.nodeAtIn(c, pos); found != InvalidNode {
			return found
		}
	}
	return n
}

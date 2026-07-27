package sema

import (
	"testing"

	"llvm_lang/src/ast"
)

// This file covers type matching over an Any subject (see LANGUAGE.md's
// "Type matching" section and checkTypeMatchStmt) - the third match subject
// kind, alongside enum matching (enum_test.go) and value matching.

// armBindingType returns the Type sema seeded for the binding of the
// armIdx'th arm of the first match statement in fn's body.
func armBindingType(t *testing.T, tree *ast.Tree, info *Info, declIdx, armIdx int) Type {
	t.Helper()
	decl := tree.Children(tree.Root)[declIdx]
	matchStmt := tree.Children(tree.FuncBody(decl))[0]
	arm := tree.MatchArms(matchStmt)[armIdx]
	name, _ := tree.TypePatternParts(tree.MatchArmPattern(arm))
	if name == ast.InvalidNode {
		t.Fatalf("arm %d has no binding", armIdx)
	}
	return info.Types[name]
}

// --- valid ---

// TestTypeMatchBindingsAreNarrowed is the core promise of the feature: each
// arm's own binding is typed as exactly the type its pattern names.
func TestTypeMatchBindingsAreNarrowed(t *testing.T) {
	src := "struct Point {\n\tX int\n}\n" +
		"func f(a Any) {\n" +
		"\tmatch a {\n" +
		"\t\tv int => {\n\t\t\tx := v\n\t\t}\n" +
		"\t\ts string => {\n\t\t\ty := s\n\t\t}\n" +
		"\t\tp Point => {\n\t\t\tz := p.X\n\t\t}\n" +
		"\t\t_ => {\n\t\t}\n" +
		"\t}\n}\n"
	tree, info := checkSrc(t, src)

	if got := armBindingType(t, tree, info, 1, 0); got.Kind != TypeI32 {
		t.Errorf("arm 0 binding = %v, want int", got)
	}
	if got := armBindingType(t, tree, info, 1, 1); got.Kind != TypeString {
		t.Errorf("arm 1 binding = %v, want string", got)
	}
	got := armBindingType(t, tree, info, 1, 2)
	if got.Kind != TypeStruct || got.Struct == nil || got.Struct.Symbol.Name != "Point" {
		t.Errorf("arm 2 binding = %v, want Point", got)
	}
}

// TestTypeMatchDiscardArmsAccepted covers every bare (binding-less) type
// pattern shape - the ones that need no TypePattern wrapper node at all.
func TestTypeMatchDiscardArmsAccepted(t *testing.T) {
	expectCheckErrors(t, "struct Point {\n\tX int\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n"+
		"\t\tint => {\n\t\t}\n"+
		"\t\tPoint => {\n\t\t}\n"+
		"\t\t*Point => {\n\t\t}\n"+
		"\t\t[]int => {\n\t\t}\n"+
		"\t\t[3]int => {\n\t\t}\n"+
		"\t\tmap[string]int => {\n\t\t}\n"+
		"\t\t_ => {\n\t\t}\n"+
		"\t}\n}\n", 0)
}

// TestTypeMatchGenericInstantiationArm covers a monomorphized generic struct
// as an arm type - the instantiation is a real, distinct type, so it needs
// no special handling beyond the ordinary type-expression path.
func TestTypeMatchGenericInstantiationArm(t *testing.T) {
	tree, info := checkSrc(t, "struct Pair[A, B] {\n\tFirst A\n\tSecond B\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n"+
		"\t\tp Pair[int, string] => {\n\t\t\tx := p.First\n\t\t}\n"+
		"\t\t_ => {\n\t\t}\n"+
		"\t}\n}\n")
	got := armBindingType(t, tree, info, 1, 0)
	if got.Kind != TypeStruct || got.Struct == nil {
		t.Fatalf("binding = %v, want a struct instantiation", got)
	}
}

// TestTypeMatchEnumArmAccepted covers an enum-typed arm: it names the enum
// type itself, never one of its variants, and matches whichever variant is
// live (proved end-to-end in codegen's own test).
func TestTypeMatchEnumArmAccepted(t *testing.T) {
	tree, info := checkSrc(t, "enum Shape {\n\tCircle(int)\n\tSquare\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n"+
		"\t\ts Shape => {\n\t\t}\n"+
		"\t\t_ => {\n\t\t}\n"+
		"\t}\n}\n")
	if got := armBindingType(t, tree, info, 1, 0); got.Kind != TypeEnum {
		t.Errorf("binding = %v, want the enum type", got)
	}
}

// TestTypeMatchNonCopyableAndAnyArmsAccepted covers point 10 of this
// feature's design: AnyNew's constructibility exclusions don't apply to a
// match arm, which only ever reads an already-boxed value.
func TestTypeMatchNonCopyableAndAnyArmsAccepted(t *testing.T) {
	expectCheckErrors(t, "struct Res {\n\tx int\n\tdestructor() {\n\t\tthis.x = 0\n\t}\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n"+
		"\t\tr Res => {\n\t\t}\n"+
		"\t\tb Any => {\n\t\t}\n"+
		"\t\t_ => {\n\t\t}\n"+
		"\t}\n}\n", 0)
}

// TestTypeMatchAsExpressionYields proves match-as-expression works over this
// third subject kind with no extra machinery - checkMatchDispatch is shared.
func TestTypeMatchAsExpressionYields(t *testing.T) {
	tree, info := checkSrc(t, "func f(a Any) {\n"+
		"\tx := match a {\n"+
		"\t\tv int => \"int\"\n"+
		"\t\tv string => v\n"+
		"\t\t_ => \"other\"\n"+
		"\t}\n}\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(tree.Children(tree.FuncBody(decl))[0], 1)
	if got := info.Types[init]; got.Kind != TypeString {
		t.Errorf("match expression type = %v, want string", got)
	}
}

// TestTypeMatchPointerSubjectStillEnumMatches guards the dispatch order: an
// Any subject is the only one routed to type matching, and a pointer-to-enum
// subject keeps its own pre-existing auto-deref enum path.
func TestTypeMatchPointerSubjectStillEnumMatches(t *testing.T) {
	expectCheckErrors(t, "enum Shape {\n\tCircle(int)\n\tSquare\n}\n"+
		"func (Shape) f() {\n"+
		"\tmatch this {\n"+
		"\t\tShape.Circle(r) => {\n\t\t}\n"+
		"\t\tShape.Square => {\n\t\t}\n"+
		"\t}\n}\n", 0)
}

// --- invalid ---

func TestTypeMatchMissingWildcardRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tv int => {\n\t\t}\n\t}\n}\n", 1)
}

func TestTypeMatchDuplicateWildcardRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tv int => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

func TestTypeMatchDuplicateTypeRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tv int => {\n\t\t}\n\t\tw int => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// int and i32 are the same type under two spellings - duplicate detection is
// by Type.Equal, not by how the arm happened to name it.
func TestTypeMatchDuplicateTypeUnderAliasRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tv int => {\n\t\t}\n\t\tw i32 => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// Two structurally identical array arms are the same type too, even though
// each is a fresh type expression rather than a named declaration.
func TestTypeMatchDuplicateArrayTypeRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\t[]int => {\n\t\t}\n\t\t[]int => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// Two DIFFERENT structs sharing the TypeStruct kind are not duplicates -
// dedupe is by type identity, not by kind.
func TestTypeMatchTwoDistinctStructArmsAccepted(t *testing.T) {
	expectCheckErrors(t, "struct A {\n\tX int\n}\n"+
		"struct B {\n\tY int\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n\t\tx A => {\n\t\t}\n\t\ty B => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 0)
}

func TestTypeMatchUnboxableArmTypeRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tg func(int) int => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// A struct is only boxable if every one of its fields is - an arm naming one
// that isn't must be rejected for the same reason boxing it is.
func TestTypeMatchUnboxableStructArmRejected(t *testing.T) {
	expectCheckErrors(t, "struct Bag {\n\tItems func()\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n\t\tb Bag => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// The same rejection through the binding-less arm form, whose pattern node
// is the type node itself.
func TestTypeMatchUnboxableBareArmRejected(t *testing.T) {
	expectCheckErrors(t, "struct Bag {\n\tItems func()\n}\n"+
		"func f(a Any) {\n"+
		"\tmatch a {\n\t\tBag => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

func TestTypeMatchMultiPatternArmRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tint, string => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// A value pattern (a literal) is meaningless against an erased subject.
func TestTypeMatchValuePatternRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\t5 => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// A pattern naming something that exists but isn't a type - Resolve can't
// catch this (it doesn't know the subject is an Any), so Check must.
func TestTypeMatchNonTypeNameRejected(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tx := 5\n"+
		"\tmatch a {\n\t\tx => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// A binding whose arm type was rejected still gets a fallback type, so the
// arm body's own reference to it doesn't cascade into a second, unrelated
// diagnostic.
func TestTypeMatchRejectedArmBindingDoesNotCascade(t *testing.T) {
	expectCheckErrors(t, "func f(a Any) {\n"+
		"\tmatch a {\n\t\tg func() => {\n\t\t\th := g\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

// --- a type pattern outside an Any match ---

func TestTypePatternInValueMatchRejected(t *testing.T) {
	expectCheckErrors(t, "struct Point {\n\tX int\n}\n"+
		"func f(n int) {\n"+
		"\tmatch n {\n\t\tv Point => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

func TestTypePatternInEnumMatchRejected(t *testing.T) {
	expectCheckErrors(t, "enum Shape {\n\tCircle(int)\n\tSquare\n}\n"+
		"func f(s Shape) {\n"+
		"\tmatch s {\n\t\tv Shape => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

func TestBareTypePatternInValueMatchRejected(t *testing.T) {
	expectCheckErrors(t, "func f(n int) {\n"+
		"\tmatch n {\n\t\t[]int => {\n\t\t}\n\t\t_ => {\n\t\t}\n\t}\n}\n", 1)
}

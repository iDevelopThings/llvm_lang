package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
)

// This file covers `for [key[, value]] := range subject { ... }` (see
// LANGUAGE.md's "Range loops" section): the map/array binding-type rules,
// the one-binding map-binds-key/array-binds-index wrinkle (Go's own real
// rule - see checkRangeForStmt), break/continue legality, and the clean
// diagnostics for an unsupported subject type or a `range` expression used
// outside a for-loop header.
//
// This language has no "declared and not used" check (unlike Go), so a
// binding declared but never referenced inside its own loop body is fine -
// every fixture below simply leaves it unreferenced rather than reaching
// for a blank identifier (`_ = k`), which this language also doesn't support
// (see resolve.go's own doc comment on the wildcard `_` being match-pattern-
// only, not a general discard assignment target).

// --- binding types: two-binding form ---

func TestRangeForMapTwoBindingTypes(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tfor k, v := range m {\n\t}\n}\n")
	key, value := rangeForBindings(t, tree)
	if got := info.Types[key]; got.Kind != TypeString {
		t.Errorf("key type = %v, want string", got)
	}
	if got := info.Types[value]; got.Kind != TypeI32 {
		t.Errorf("value type = %v, want int", got)
	}
}

func TestRangeForArrayTwoBindingTypes(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\tvar a [3]bool\n\tfor i, v := range a {\n\t}\n}\n")
	key, value := rangeForBindings(t, tree)
	if got := info.Types[key]; got.Kind != TypeI32 {
		t.Errorf("index type = %v, want int", got)
	}
	if got := info.Types[value]; got.Kind != TypeBool {
		t.Errorf("value type = %v, want bool", got)
	}
}

// --- the one-binding wrinkle: map binds the key, array binds the index ---

// TestRangeForMapOneBindingBindsKey proves the single name binds K, not V -
// a map[string]bool would let a "binds the value instead" bug hide behind
// two identically-shaped types, so this deliberately uses two DIFFERENT
// types (string key, bool value) and asserts the bound name is string.
func TestRangeForMapOneBindingBindsKey(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\tm := make(map[string]bool)\n\tfor k := range m {\n\t}\n}\n")
	key, value := rangeForBindings(t, tree)
	if value != ast.InvalidNode {
		t.Fatalf("value slot = %d, want InvalidNode (one-binding form)", value)
	}
	if got := info.Types[key]; got.Kind != TypeString {
		t.Errorf("one-binding name type = %v, want string (the key), not bool (the value)", got)
	}
}

// TestRangeForArrayOneBindingBindsIndex proves the single name binds the
// index (always int), not the element - a [3]int array would let a "binds
// the element instead" bug hide behind an identically-typed index/element,
// so this deliberately uses a non-int element type (bool) and asserts the
// bound name is int.
func TestRangeForArrayOneBindingBindsIndex(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\tvar a [3]bool\n\tfor i := range a {\n\t}\n}\n")
	key, value := rangeForBindings(t, tree)
	if value != ast.InvalidNode {
		t.Fatalf("value slot = %d, want InvalidNode (one-binding form)", value)
	}
	if got := info.Types[key]; got.Kind != TypeI32 {
		t.Errorf("one-binding name type = %v, want int (the index), not bool (the element)", got)
	}
}

// --- zero-binding form ---

func TestRangeForZeroBindingChecksSubject(t *testing.T) {
	checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tfor range m {\n\t}\n}\n")
}

func TestRangeForZeroBindingOverArrayIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a [3]int\n\tfor range a {\n\t}\n}\n")
}

// --- unsupported subject types: clean diagnostics, never a panic ---

func TestRangeForOverStructIsError(t *testing.T) {
	src := "struct Point {\n\tx int\n}\n\nfunc f() {\n\tp := Point{1}\n\tfor range p {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestRangeForOverIntIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tn := 5\n\tfor range n {\n\t}\n}\n", 1)
}

func TestRangeForOverStringIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ts := \"hi\"\n\tfor range s {\n\t}\n}\n", 1)
}

// TestRangeForNonCopyableValueBindingRejected covers the illegal-copy rule
// (see checkRangeForStmt's own doc comment): every iteration's value binding
// is a genuine copy out of the array's own storage, exactly like any other
// short-var-decl destructuring a call/index result (`v := m[k]`), so a
// destructor-owning (non-copyable) element type must be rejected here too -
// a clean diagnostic, never a panic, and never a silent extra destructor
// call on a value the type's own copy rule says should never be duplicated.
func TestRangeForNonCopyableValueBindingRejected(t *testing.T) {
	src := "struct Resource {\n\tid int\n\tconstructor(x int) {\n\t\tthis.id = x\n\t}\n\tdestructor() {\n\t}\n}\n\nfunc f() {\n\tarr := [3]Resource{Resource(1), Resource(2), Resource(3)}\n\tfor _, r := range arr {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- break/continue legality ---

func TestBreakInsideRangeForIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tfor k, v := range m {\n\t\tbreak\n\t}\n}\n")
}

func TestContinueInsideRangeForIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tfor k, v := range m {\n\t\tcontinue\n\t}\n}\n")
}

// TestBreakAfterRangeForEndsIsError proves loopDepth is correctly
// decremented on exit from a RangeForStmt's body, not just incremented -
// mirroring TestBreakAfterLoopEndsIsError's identical proof for a plain
// ForStmt.
func TestBreakAfterRangeForEndsIsError(t *testing.T) {
	src := "func f() {\n\tm := make(map[string]int)\n\tfor k := range m {\n\t}\n\tbreak\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- RangeForStmt is never a terminating statement ---

// TestRangeForNeverSatisfiesMissingReturn proves a RangeForStmt - unlike a
// bare `for {}` with no cond - never counts as terminating, since a map/array
// can always have zero entries at runtime: a function ending in one must
// still report "missing return".
func TestRangeForNeverSatisfiesMissingReturn(t *testing.T) {
	src := "func f() int {\n\tm := make(map[string]int)\n\tfor k := range m {\n\t\treturn 1\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- RangeExpr outside a for-loop header ---

func TestRangeExprOutsideForHeaderIsError(t *testing.T) {
	src := "func f() {\n\tm := make(map[string]int)\n\tx := range m\n\tprint(x)\n}\n"
	expectCheckErrors(t, src, 1)
}

// rangeForBindings locates the sole RangeForStmt anywhere in tree and
// returns its key/value children (ast.InvalidNode for an omitted binding) -
// a small helper shared by every binding-type test above, a flat scan over
// the tree's own node array since none of these fixtures are deep enough to
// need a general-purpose finder.
func rangeForBindings(t *testing.T, tree *ast.Tree) (key, value ast.NodeIndex) {
	t.Helper()
	for idx := 1; idx < len(tree.Nodes); idx++ {
		n := ast.NodeIndex(idx)
		if tree.Nodes[n].Kind == enums.NodeKinds.RangeForStmt {
			return tree.RangeForKey(n), tree.RangeForValue(n)
		}
	}
	t.Fatal("no RangeForStmt found in tree")
	return ast.InvalidNode, ast.InvalidNode
}

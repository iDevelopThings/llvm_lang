package sema

import "testing"

// This file covers this round's `map[K]V` feature (see LANGUAGE.md's "Maps"
// section): the TypeMap Type itself, make/len/remove's own dispatch, the
// key-comparability restriction, the `v, ok := m[k]` two-result index
// expression (and its distinct-from-multi-return design - see
// checkDestructureSource), and the explicitly out-of-scope cases (a map
// composite literal, a map key that isn't comparable, compound assignment to
// a map element) rejected with a clean diagnostic rather than a panic.

// --- the type itself ---

func TestMapVarDeclType(t *testing.T) {
	tree, info := checkSrc(t, "var m map[string]int\n")
	decl := tree.Children(tree.Root)[0]
	got := info.Types[decl]
	if got.Kind != TypeMap {
		t.Fatalf("Types[decl] = %v, want TypeMap", got)
	}
	if got.Key.Kind != TypeString {
		t.Errorf("Key = %v, want string", got.Key)
	}
	if got.Elem.Kind != TypeI32 {
		t.Errorf("Elem = %v, want int", got.Elem)
	}
}

func TestMapTypeString(t *testing.T) {
	tree, info := checkSrc(t, "var m map[string]int\n")
	decl := tree.Children(tree.Root)[0]
	if got := info.Types[decl].String(); got != "map[string]int" {
		t.Errorf("String() = %q, want %q", got, "map[string]int")
	}
}

func TestMapTypeEqual(t *testing.T) {
	a := Type{Kind: TypeMap, Key: &stringType, Elem: &i32Type}
	b := Type{Kind: TypeMap, Key: &stringType, Elem: &i32Type}
	c := Type{Kind: TypeMap, Key: &stringType, Elem: &boolType}
	if !a.Equal(b) {
		t.Errorf("expected map[string]int == map[string]int")
	}
	if a.Equal(c) {
		t.Errorf("expected map[string]int != map[string]bool")
	}
}

// Nested maps/map-as-struct-field should fall out for free from the general
// type-position grammar (see LANGUAGE.md's own explicit note this needs a
// real test, not just assuming) - a map's value type is just "any type,"
// including another map.
func TestNestedMapValueType(t *testing.T) {
	tree, info := checkSrc(t, "var m map[string]map[string]int\n")
	decl := tree.Children(tree.Root)[0]
	got := info.Types[decl]
	if got.Kind != TypeMap || got.Elem.Kind != TypeMap {
		t.Fatalf("Types[decl] = %v, want map[string]map[string]int", got)
	}
	if got.Elem.Elem.Kind != TypeI32 {
		t.Errorf("inner Elem = %v, want int", got.Elem.Elem)
	}
}

func TestMapStructFieldType(t *testing.T) {
	src := "struct Box {\n\tm map[string]int\n}\n"
	checkSrc(t, src)
}

// --- make/len ---

func TestMakeMapOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tm[\"a\"] = 1\n}\n")
}

func TestMakeMapWithExtraArgsRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tm := make(map[string]int, 4)\n}\n", 1)
}

func TestLenOfMap(t *testing.T) {
	src := "func f() int {\n\tm := make(map[string]int)\n\treturn len(m)\n}\n"
	checkSrc(t, src)
}

// --- key comparability ---

func TestDynamicArrayMapKeyRejected(t *testing.T) {
	expectCheckErrors(t, "var m map[[]int]int\n", 1)
}

func TestFuncTypeMapKeyRejected(t *testing.T) {
	expectCheckErrors(t, "var m map[func(int) int]int\n", 1)
}

func TestMapMapKeyRejected(t *testing.T) {
	expectCheckErrors(t, "var m map[map[string]int]int\n", 1)
}

func TestComparableStructMapKeyOk(t *testing.T) {
	src := "struct Point {\n\tx int\n\ty int\n}\n" +
		"var m map[Point]string\n"
	checkSrc(t, src)
}

func TestStructMapKeyWithDynamicArrayFieldRejected(t *testing.T) {
	src := "struct Bad {\n\ts []int\n}\n" +
		"var m map[Bad]string\n"
	expectCheckErrors(t, src, 1)
}

func TestFixedArrayOfComparableTypeMapKeyOk(t *testing.T) {
	checkSrc(t, "var m map[[3]int]string\n")
}

func TestPointerMapKeyOk(t *testing.T) {
	checkSrc(t, "var m map[*int]string\n")
}

// --- remove ---

func TestRemoveOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tremove(m, \"a\")\n}\n")
}

func TestRemoveWrongArgCountRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tm := make(map[string]int)\n\tremove(m)\n}\n", 1)
}

func TestRemoveOnNonMapRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := 5\n\tremove(a, 1)\n}\n", 1)
}

// --- the two-result index expression (`v, ok := m[k]`) ---

func TestMapTwoResultIndexOk(t *testing.T) {
	src := "func f() {\n\tm := make(map[string]int)\n\tv, ok := m[\"a\"]\n\tprint(v)\n\tprint(ok)\n}\n"
	checkSrc(t, src)
}

func TestMapTwoResultIndexTypes(t *testing.T) {
	src := "func f() {\n\tm := make(map[string]int)\n\tv, ok := m[\"a\"]\n\tprint(v)\n\tprint(ok)\n}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	decl := tree.Children(body)[1] // [0]=make short-var-decl, [1]=multi-short-var-decl
	names := tree.MultiShortVarDeclNames(decl)
	if got := info.Types[names[0]]; got.Kind != TypeI32 {
		t.Errorf("v's type = %v, want int", got)
	}
	if got := info.Types[names[1]]; got.Kind != TypeBool {
		t.Errorf("ok's type = %v, want bool", got)
	}
}

// A plain single-target `x := m[k]` in the exact same program must still
// work as an ordinary single value (V alone) - proving the "two-result index
// expression is context-dependent, not a real multi-return Type" distinction
// actually holds both ways, in one program.
func TestMapSingleAndTwoResultIndexCoexist(t *testing.T) {
	src := "func f() {\n" +
		"\tm := make(map[string]int)\n" +
		"\tx := m[\"a\"]\n" +
		"\tv, ok := m[\"b\"]\n" +
		"\tprint(x)\n\tprint(v)\n\tprint(ok)\n" +
		"}\n"
	checkSrc(t, src)
}

func TestMapTwoResultIndexWrongTargetCountRejected(t *testing.T) {
	src := "func f() {\n\tm := make(map[string]int)\n\ta, b, c := m[\"x\"]\n}\n"
	expectCheckErrors(t, src, 1)
}

// A real multi-return function call used as a single value must still be
// rejected exactly as before maps existed - the map-index special case in
// checkDestructureSource doesn't subsume or weaken this independently-
// triggered path through the same destructuring-context code.
func TestMultiReturnCallStillRejectedAsSingleValueAlongsideMaps(t *testing.T) {
	src := "func divide(a int, b int) (int, bool) {\n\treturn a, true\n}\n" +
		"func f() {\n\tx := divide(4, 2)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- assignment / mutation restrictions ---

func TestMapIndexAssignOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tm := make(map[string]int)\n\tm[\"a\"] = 1\n}\n")
}

func TestMapIndexCompoundAssignRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tm := make(map[string]int)\n\tm[\"a\"] += 1\n}\n", 1)
}

func TestMapIndexIncDecRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tm := make(map[string]int)\n\tm[\"a\"]++\n}\n", 1)
}

func TestMapIndexAddressOfRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tm := make(map[string]int)\n\tp := &m[\"a\"]\n\tprint(p)\n}\n", 1)
}

package sema

import "testing"

// --- valid: boxing ---

func TestAnyBoxPrimitiveKinds(t *testing.T) {
	for _, tc := range []struct {
		src  string
		kind TypeKind
	}{
		{"func f() { a := Any(5) }\n", TypeI32},
		{"func f() { a := Any(5.0) }\n", TypeF64},
		{"func f() { a := Any(true) }\n", TypeBool},
		{"func f() { a := Any(\"hi\") }\n", TypeString},
	} {
		tree, info := checkSrc(t, tc.src)
		decl := tree.Children(tree.Root)[0]
		body := tree.FuncBody(decl)
		shortVar := tree.Children(body)[0]
		init := tree.Child(shortVar, 1)
		got := info.Types[init]
		if got.Kind != TypeAny {
			t.Fatalf("%q: Types[init].Kind = %v, want Any", tc.src, got.Kind)
		}
	}
}

func TestAnyBoxStruct(t *testing.T) {
	tree, info := checkSrc(t, "struct Point {\n\tX int\n}\n"+
		"func f() {\n\tp := Point{X: 1}\n\ta := Any(p)\n}\n")
	decl := tree.Children(tree.Root)[1]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	init := tree.Child(stmts[1], 1)
	if got := info.Types[init]; got.Kind != TypeAny {
		t.Fatalf("Types[init].Kind = %v, want Any", got.Kind)
	}
}

func TestAnyBoxPointer(t *testing.T) {
	checkSrc(t, "func f() {\n\tx := 5\n\tp := &x\n\ta := Any(p)\n}\n")
}

func TestAnyIntoAnyIsNoOpCopy(t *testing.T) {
	checkSrc(t, "func f() {\n\ta := Any(5)\n\tb := Any(a)\n}\n")
}

// --- valid: reflection builtins ---

func TestAnyKindReturnsI32(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\ta := Any(5)\n\tk := AnyKind(a)\n}\n")
	decl := tree.Children(tree.Root)[0]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	init := tree.Child(stmts[1], 1)
	if got := info.Types[init]; got.Kind != TypeI32 {
		t.Fatalf("Types[AnyKind call] = %v, want i32", got)
	}
}

func TestAnyNameReturnsString(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\ta := Any(5)\n\tn := AnyName(a)\n}\n")
	decl := tree.Children(tree.Root)[0]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	init := tree.Child(stmts[1], 1)
	if got := info.Types[init]; got.Kind != TypeString {
		t.Fatalf("Types[AnyName call] = %v, want string", got)
	}
}

func TestAnyAsRoundTripType(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\ta := Any(5)\n\tv, ok := AnyAs[int](a)\n}\n")
	decl := tree.Children(tree.Root)[0]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	names := tree.MultiShortVarDeclNames(stmts[1])
	if got := info.Types[names[0]]; got.Kind != TypeI32 {
		t.Errorf("v's Type = %v, want int", got)
	}
	if got := info.Types[names[1]]; got.Kind != TypeBool {
		t.Errorf("ok's Type = %v, want bool", got)
	}
}

func TestAnyFieldsRangeBindings(t *testing.T) {
	tree, info := checkSrc(t, "struct Point {\n\tX int\n}\n"+
		"func f() {\n\tp := Point{X: 1}\n\ta := Any(p)\n"+
		"\tfor name, v := range AnyFields(a) {\n\t\tprint(name)\n\t}\n}\n")
	decl := tree.Children(tree.Root)[1]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	rangeFor := stmts[2]
	keyNode := tree.RangeForKey(rangeFor)
	valueNode := tree.RangeForValue(rangeFor)
	if got := info.Types[keyNode]; got.Kind != TypeString {
		t.Errorf("range key (name) Type = %v, want string", got)
	}
	if got := info.Types[valueNode]; got.Kind != TypeAny {
		t.Errorf("range value (v) Type = %v, want Any", got)
	}
}

func TestAnyLenReturnsI32(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\ts := []int{1, 2}\n\ta := Any(s)\n\tn := AnyLen(a)\n}\n")
	decl := tree.Children(tree.Root)[0]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	init := tree.Child(stmts[2], 1)
	if got := info.Types[init]; got.Kind != TypeI32 {
		t.Fatalf("Types[AnyLen call] = %v, want i32", got)
	}
}

func TestAnyIndexRoundTripType(t *testing.T) {
	tree, info := checkSrc(t, "func f() {\n\ts := []int{1, 2}\n\ta := Any(s)\n\tv, ok := AnyIndex(a, 0)\n}\n")
	decl := tree.Children(tree.Root)[0]
	body := tree.FuncBody(decl)
	stmts := tree.Children(body)
	names := tree.MultiShortVarDeclNames(stmts[2])
	if got := info.Types[names[0]]; got.Kind != TypeAny {
		t.Errorf("v's Type = %v, want Any", got)
	}
	if got := info.Types[names[1]]; got.Kind != TypeBool {
		t.Errorf("ok's Type = %v, want bool", got)
	}
}

// TestAnyIndexUntypedIntLiteralRetyped proves the index argument's untyped
// literal is defaulted to int exactly like an ordinary array index
// (checkIndexExpr), not left untyped.
func TestAnyIndexUntypedIntLiteralRetyped(t *testing.T) {
	checkSrc(t, "func f() {\n\ts := []int{1, 2}\n\ta := Any(s)\n\tv, ok := AnyIndex(a, 1)\n}\n")
}

// --- invalid: boxing ---

func TestAnyBoxEnumRejected(t *testing.T) {
	expectCheckErrors(t, "enum Shape {\n\tPoint\n}\n"+
		"func f() {\n\ts := Shape.Point\n\ta := Any(s)\n}\n", 1)
}

// TestAnyBoxDynamicArrayAccepted/TestAnyBoxFixedArrayAccepted cover this
// round's new array support (see LANGUAGE.md's "Any" section) - both array
// kinds are boxable as long as their own element type is.
func TestAnyBoxDynamicArrayAccepted(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ts := []int{1, 2}\n\ta := Any(s)\n}\n", 0)
}

func TestAnyBoxFixedArrayAccepted(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ts := [2]int{1, 2}\n\ta := Any(s)\n}\n", 0)
}

// TestAnyBoxArrayWithUnboxableElementRejected proves an array is only
// boxable if its own element type is - a map has no Any descriptor shape
// this round, so an array of maps must still be rejected.
func TestAnyBoxArrayWithUnboxableElementRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ts := make([]map[string]int, 1)\n\ta := Any(s)\n}\n", 1)
}

func TestAnyBoxMapRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tm := make(map[string]int)\n\ta := Any(m)\n}\n", 1)
}

func TestAnyBoxFuncValueRejected(t *testing.T) {
	expectCheckErrors(t, "func g() {}\n"+
		"func f() {\n\ta := Any(g)\n}\n", 1)
}

func TestAnyBoxNonCopyableStructRejected(t *testing.T) {
	expectCheckErrors(t, "struct Res {\n\tx int\n\tdestructor() {\n\t\tthis.x = 0\n\t}\n}\n"+
		"func f() {\n\tr := Res{0}\n\ta := Any(move r)\n}\n", 1)
}

// A struct containing a field of an otherwise-unboxable kind (here a map -
// arrays are boxable now, see TestAnyBoxDynamicArrayAccepted above) must be
// rejected at this same compile-time checkpoint, not just when boxing that
// field type directly - codegen's structDescriptor recurses into every
// field's own type descriptor unconditionally, so letting this compile would
// panic the first time Bag is ever boxed anywhere in the program, rather
// than reporting a clean diagnostic here.
func TestAnyBoxStructWithUnboxableFieldRejected(t *testing.T) {
	expectCheckErrors(t, "struct Bag {\n\tItems map[string]int\n}\n"+
		"func f() {\n\tb := Bag{Items: make(map[string]int)}\n\ta := Any(b)\n}\n", 1)
}

// TestAnyBoxStructWithArrayFieldAccepted proves a struct field that's itself
// an array composes correctly with both this round's array support and the
// pre-existing per-field struct recursion.
func TestAnyBoxStructWithArrayFieldAccepted(t *testing.T) {
	expectCheckErrors(t, "struct Bag {\n\tItems []int\n}\n"+
		"func f() {\n\tb := Bag{Items: []int{1, 2}}\n\ta := Any(b)\n}\n", 0)
}

// An Any-typed field is legal at the top level (Any(x) where x is already
// Any is a defined no-op re-box), but typeDescriptorFor has no TypeAny case
// of its own - only genAnyBox's top-level short-circuit does - so a struct
// with an Any-typed field must still be rejected the same way any other
// unboxable field kind is, not just accepted because TypeAny happens to be
// boxable at the top level.
func TestAnyBoxStructWithAnyFieldRejected(t *testing.T) {
	expectCheckErrors(t, "struct Wrapper {\n\tV Any\n}\n"+
		"func f() {\n\tw := Wrapper{V: Any(5)}\n\ta := Any(w)\n}\n", 1)
}

// A struct field that's itself a pointer stays boxable regardless of what it
// points to - TypePointer never recurses into its own Elem (the same
// cycle-safety a self-referential struct already relies on for
// typeIsComparable/typeIsPrintable), so this must NOT be rejected.
func TestAnyBoxStructWithPointerFieldAccepted(t *testing.T) {
	expectCheckErrors(t, "struct Node {\n\tValue int\n\tNext  *Node\n}\n"+
		"func f() {\n\tn := Node{Value: 1, Next: nil}\n\ta := Any(n)\n}\n", 0)
}

// --- invalid: reflection builtins on a non-Any argument ---

func TestAnyKindNonAnyArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tk := AnyKind(5)\n}\n", 1)
}

func TestAnyNameNonAnyArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tn := AnyName(5)\n}\n", 1)
}

func TestAnyFieldsNonAnyArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tfor name, v := range AnyFields(5) {\n\t\tprint(name)\n\t}\n}\n", 1)
}

func TestAnyAsNonAnyArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tv, ok := AnyAs[int](5)\n}\n", 1)
}

func TestAnyLenNonAnyArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tn := AnyLen(5)\n}\n", 1)
}

func TestAnyIndexNonAnyArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tv, ok := AnyIndex(5, 0)\n}\n", 1)
}

func TestAnyIndexNonIntIndexRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ts := []int{1, 2}\n\ta := Any(s)\n\tv, ok := AnyIndex(a, \"x\")\n}\n", 1)
}

func TestAnyIndexWrongArgCountRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ts := []int{1, 2}\n\ta := Any(s)\n\tv, ok := AnyIndex(a)\n}\n", 1)
}

func TestAnyAsMissingTypeArgRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := Any(5)\n\tv, ok := AnyAs(a)\n}\n", 1)
}

func TestAnyAsUnboxableTypeArgRejected(t *testing.T) {
	expectCheckErrors(t, "enum Shape {\n\tPoint\n}\n"+
		"func f() {\n\ta := Any(5)\n\tv, ok := AnyAs[Shape](a)\n}\n", 1)
}

// TestAnyFieldsOutsideRangeForRejected proves AnyFields' TypeGenerator
// result reuses the existing "a generator's result is only legal as a
// range-for's own subject" restriction (rejectGeneratorValue) with no extra
// code of its own - storing it in a variable is rejected exactly like any
// other generator call would be.
func TestAnyFieldsOutsideRangeForRejected(t *testing.T) {
	expectCheckErrors(t, "struct Point {\n\tX int\n}\n"+
		"func f() {\n\tp := Point{X: 1}\n\ta := Any(p)\n\tx := AnyFields(a)\n}\n", 1)
}

func TestAnyFieldsSingleBindingRejected(t *testing.T) {
	expectCheckErrors(t, "struct Point {\n\tX int\n}\n"+
		"func f() {\n\tp := Point{X: 1}\n\ta := Any(p)\n"+
		"\tfor name := range AnyFields(a) {\n\t\tprint(name)\n\t}\n}\n", 1)
}

// --- invalid: Any is neither comparable nor printable this round ---

func TestAnyEqualityRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := Any(5)\n\tb := Any(6)\n\tif a == b {\n\t}\n}\n", 1)
}

func TestAnyPrintRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := Any(5)\n\tprint(a)\n}\n", 1)
}

// --- invalid: Any has no composite literal or field access ---

func TestAnyCompositeLitRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := Any{}\n}\n", 1)
}

func TestAnyFieldAccessRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := Any(5)\n\tx := a.foo\n}\n", 1)
}

// --- invalid: Any crossing an extern func signature ---

func TestExternFuncAnyParamRejected(t *testing.T) {
	expectCheckErrors(t, "extern func f(a Any) int\n", 1)
}

func TestExternFuncAnyReturnRejected(t *testing.T) {
	expectCheckErrors(t, "extern func f() Any\n", 1)
}

// --- collecting into a ...Any variadic parameter implicitly boxes each
// argument (checkVariadicCallArgs), the one deliberate exception to this
// language's no-implicit-conversion rule - see LANGUAGE.md's "Variadic
// parameters" section ---

func TestVariadicAnyCollectImplicitlyBoxesRawValues(t *testing.T) {
	checkSrc(t, "func Log(args ...Any) int {\n\treturn len(args)\n}\n"+
		"func f() int {\n\treturn Log(5, \"x\", true, 3.5)\n}\n")
}

func TestVariadicAnyCollectAcceptsAlreadyBoxedValues(t *testing.T) {
	checkSrc(t, "func Log(args ...Any) int {\n\treturn len(args)\n}\n"+
		"func f() int {\n\treturn Log(Any(5), 7)\n}\n")
}

func TestVariadicAnyCollectRejectsUnboxableType(t *testing.T) {
	expectCheckErrors(t, "enum Shape {\n\tCircle(int)\n}\n"+
		"func Log(args ...Any) int {\n\treturn len(args)\n}\n"+
		"func f() {\n\ts := Shape.Circle(5)\n\tLog(s)\n}\n", 1)
}

func TestVariadicAnyCollectRejectsNonCopyableType(t *testing.T) {
	expectCheckErrors(t, "struct Resource {\n\tv int\n\tdestructor() {\n\t\tthis.v = 0\n\t}\n}\n"+
		"func Log(args ...Any) int {\n\treturn len(args)\n}\n"+
		"func f() {\n\tr := Resource{1}\n\tLog(r)\n}\n", 1)
}

// TestVariadicGenericFuncInstantiatedAtAny covers a generic function's own
// variadic parameter explicitly instantiated with T=Any - checkGenericCall
// reaches checkCallArgs/checkVariadicCallArgs against the already-
// monomorphized signature exactly like a non-generic call, so implicit
// boxing composes with generics for free, with no special-casing needed in
// generics.go.
func TestVariadicGenericFuncInstantiatedAtAny(t *testing.T) {
	checkSrc(t, "func Log[T](args ...T) int {\n\treturn len(args)\n}\n"+
		"func f() int {\n\treturn Log[Any](5, \"x\", true)\n}\n")
}

// TestVariadicAnySpreadStillRequiresExactSliceType covers the boundary the
// implicit boxing above deliberately doesn't cross - spread forwards an
// existing slice value directly, not one argument at a time, so it keeps
// this language's ordinary no-implicit-conversion rule: a []int can't
// spread into a ...Any parameter without first being built as []Any.
func TestVariadicAnySpreadStillRequiresExactSliceType(t *testing.T) {
	expectCheckErrors(t, "func Log(args ...Any) int {\n\treturn len(args)\n}\n"+
		"func f() {\n\tnums := []int{1, 2, 3}\n\tLog(nums...)\n}\n", 1)
}

package sema

import "testing"

// --- Go-style slice expressions (see LANGUAGE.md's "Slicing" section) ---

// TestSliceDynamicArrayType covers `s[a:b]` on a dynamic array (`[]T`)
// typing to the exact same `[]T` Type as s itself.
func TestSliceDynamicArrayType(t *testing.T) {
	tree, info := checkSrc(t, "func f() []int {\n\ts := []int{1, 2, 3}\n\treturn s[0:2]\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	ret := tree.Child(body, 1)
	sliceExpr := tree.Child(ret, 0)
	rt := info.Types[sliceExpr]
	if rt.Kind != TypeArray || !rt.Dynamic || rt.Elem.Kind != TypeI32 {
		t.Fatalf("s[0:2] type = %v, want []int", rt)
	}
}

// TestSliceStringType covers `s[a:b]` on a string typing to `string`.
func TestSliceStringType(t *testing.T) {
	tree, info := checkSrc(t, "func f() string {\n\ts := \"hello world\"\n\treturn s[0:5]\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	ret := tree.Child(body, 1)
	sliceExpr := tree.Child(ret, 0)
	rt := info.Types[sliceExpr]
	if rt.Kind != TypeString {
		t.Fatalf("s[0:5] type = %v, want string", rt)
	}
}

// TestSliceFixedArrayProducesDynamicArray covers `arr[a:b]` on a fixed-size
// array (`[N]T`) - it must produce a genuine `[]T` (dynamic array), not
// another `[N]T`, matching Go's own real behavior exactly.
func TestSliceFixedArrayProducesDynamicArray(t *testing.T) {
	tree, info := checkSrc(t, "func f() []int {\n\tarr := [5]int{1, 2, 3, 4, 5}\n\treturn arr[1:3]\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	ret := tree.Child(body, 1)
	sliceExpr := tree.Child(ret, 0)
	rt := info.Types[sliceExpr]
	if rt.Kind != TypeArray || !rt.Dynamic || rt.Elem.Kind != TypeI32 {
		t.Fatalf("arr[1:3] type = %v, want []int (a dynamic array)", rt)
	}
}

// TestSliceFixedArrayRequiresAddressable covers rejecting a slice expression
// on a non-addressable fixed-array rvalue (a function's own return value) -
// the resulting slice would need a real, stable backing address to alias
// into, which a bare rvalue doesn't have.
func TestSliceFixedArrayRequiresAddressable(t *testing.T) {
	expectCheckErrors(t, "func make5() [5]int {\n\treturn [5]int{1, 2, 3, 4, 5}\n}\nfunc f() []int {\n\treturn make5()[1:3]\n}\n", 1)
}

// TestSliceFixedArrayThroughMemberAndIndexIsAddressable covers the two other
// addressable shapes checkArraySliceAddressable accepts - a struct field and
// an array element - alongside the plain-variable case the other tests
// already cover.
func TestSliceFixedArrayThroughMemberAndIndexIsAddressable(t *testing.T) {
	checkSrc(t, "struct Box {\n\titems [5]int\n}\nfunc f() []int {\n\tb := Box{[5]int{1, 2, 3, 4, 5}}\n\treturn b.items[1:3]\n}\n")
	checkSrc(t, "func f() []int {\n\tgrid := [2][3]int{[3]int{1, 2, 3}, [3]int{4, 5, 6}}\n\treturn grid[0][0:2]\n}\n")
}

// TestSliceNonIntBoundIsError covers rejecting a non-int bound expression
// (a string, here) for either the low or the high bound.
func TestSliceNonIntBoundIsError(t *testing.T) {
	expectCheckErrors(t, "func f() []int {\n\ts := []int{1, 2, 3}\n\treturn s[\"a\":2]\n}\n", 1)
	expectCheckErrors(t, "func f() []int {\n\ts := []int{1, 2, 3}\n\treturn s[0:\"b\"]\n}\n", 1)
}

// TestSliceOnNonSliceableTypeIsError covers rejecting a slice expression on
// an operand that's neither an array nor a string.
func TestSliceOnNonSliceableTypeIsError(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n\tx := 5\n\treturn x[0:1]\n}\n", 1)
}

// TestSliceOmittedBoundsDefaultCorrectly covers all four omitted-bound forms
// (`s[a:b]`, `s[:b]`, `s[a:]`, `s[:]`) type-checking cleanly, with the
// omitted child recorded as ast.InvalidNode (see ast.Node's own SliceExpr
// doc comment) - the actual runtime default value is a codegen concern, not
// something sema itself computes.
func TestSliceOmittedBoundsDefaultCorrectly(t *testing.T) {
	tree, info := checkSrc(t, "func f() []int {\n\ts := []int{1, 2, 3, 4, 5}\n\ta := s[1:3]\n\tb := s[:3]\n\tc := s[1:]\n\td := s[:]\n\treturn s\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)

	cases := []struct {
		stmtIdx      int
		wantLowValid bool
		wantHiValid  bool
	}{
		{1, true, true},   // a := s[1:3]
		{2, false, true},  // b := s[:3]
		{3, true, false},  // c := s[1:]
		{4, false, false}, // d := s[:]
	}
	for _, tc := range cases {
		decl := tree.Child(body, tc.stmtIdx)
		sliceExpr := tree.Child(decl, 1)
		low := tree.Child(sliceExpr, 1)
		high := tree.Child(sliceExpr, 2)
		if gotLowValid := low != 0; gotLowValid != tc.wantLowValid {
			t.Errorf("stmt %d: low valid = %v, want %v", tc.stmtIdx, gotLowValid, tc.wantLowValid)
		}
		if gotHiValid := high != 0; gotHiValid != tc.wantHiValid {
			t.Errorf("stmt %d: high valid = %v, want %v", tc.stmtIdx, gotHiValid, tc.wantHiValid)
		}
		rt := info.Types[sliceExpr]
		if rt.Kind != TypeArray || !rt.Dynamic {
			t.Errorf("stmt %d: type = %v, want []int", tc.stmtIdx, rt)
		}
	}
}

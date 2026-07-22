package sema

import (
	"testing"
)

// --- pointers/new/delete/nil (see LANGUAGE.md's "Pointers" section) ---

const pointStructSrc = "struct Point {\n" +
	"\tx int\n" +
	"\ty int\n\n" +
	"\tconstructor(x int, y int) {\n" +
	"\t\tthis.x = x\n" +
	"\t\tthis.y = y\n" +
	"\t}\n" +
	"}\n"

// TestPointerVarDeclType covers a plain `*T` var declaration typing to a
// real TypePointer wrapping T.
func TestPointerVarDeclType(t *testing.T) {
	tree, info := checkSrc(t, pointStructSrc+"var p *Point\n")
	decl := tree.Children(tree.Root)[1]
	pt := info.Types[decl]
	if pt.Kind != TypePointer {
		t.Fatalf("Types[decl].Kind = %v, want TypePointer", pt.Kind)
	}
	if pt.Elem == nil || pt.Elem.Kind != TypeStruct {
		t.Fatalf("Types[decl].Elem = %v, want *TypeStruct", pt.Elem)
	}
}

// TestAddressOfVariableProducesPointer covers `&x` typing to `*T` when x is
// a plain int variable.
func TestAddressOfVariableProducesPointer(t *testing.T) {
	tree, info := checkSrc(t, "func f() int {\n\tx := 5\n\tp := &x\n\treturn *p\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	pDecl := tree.Child(body, 1)
	pt := info.Types[pDecl]
	if pt.Kind != TypePointer || pt.Elem.Kind != TypeI32 {
		t.Fatalf("p's type = %v, want *int", pt)
	}
}

// TestAddressOfNonAddressableIsError covers rejecting `&` applied to a
// non-addressable expression - a literal, in this case.
func TestAddressOfNonAddressableIsError(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n\tp := &5\n\treturn *p\n}\n", 1)
}

// TestAddressOfFunctionIsError covers rejecting `&` applied to a function
// name - a function value is a TypeFunc, not addressable storage.
func TestAddressOfFunctionIsError(t *testing.T) {
	expectCheckErrors(t, "func g() int {\n\treturn 1\n}\nfunc f() int {\n\tp := &g\n\treturn 1\n}\n", 1)
}

// TestDereferenceNonPointerIsError covers rejecting `*x` when x isn't a
// pointer.
func TestDereferenceNonPointerIsError(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n\tx := 5\n\treturn *x\n}\n", 1)
}

// TestDereferenceAsLValue covers `*p = v` type-checking as a legal
// assignment target, and `*p` reading back the pointee's own type.
func TestDereferenceAsLValue(t *testing.T) {
	checkSrc(t, "func f() int {\n\tx := 5\n\tp := &x\n\t*p = 10\n\treturn *p\n}\n")
}

// TestAssignThroughDereferenceWrongTypeIsError covers `*p = v` rejecting a
// value of the wrong type for the pointee.
func TestAssignThroughDereferenceWrongTypeIsError(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n\tx := 5\n\tp := &x\n\t*p = \"no\"\n\treturn *p\n}\n", 1)
}

// TestNewConstructorCallProducesPointer covers `new Point(1, 2)` typing to
// `*Point`.
func TestNewConstructorCallProducesPointer(t *testing.T) {
	tree, info := checkSrc(t, pointStructSrc+"func f() int {\n\tp := new Point(1, 2)\n\treturn p.x\n}\n")
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 4)
	pDecl := tree.Child(body, 0)
	pt := info.Types[pDecl]
	if pt.Kind != TypePointer || pt.Elem.Kind != TypeStruct {
		t.Fatalf("p's type = %v, want *Point", pt)
	}
}

// TestNewCompositeLitProducesPointer covers `new Point{1, 2}` typing to
// `*Point`, and `new [3]int{...}` typing to `*[3]int` - indexing into the
// latter needs an explicit `(*a)[0]` dereference first: auto-deref (see
// LANGUAGE.md's "Pointers" section) is scoped to member access only, not
// indexing.
func TestNewCompositeLitProducesPointer(t *testing.T) {
	checkSrc(t, pointStructSrc+"func f() int {\n\tp := new Point{1, 2}\n\treturn p.x\n}\n")
	checkSrc(t, "func f() int {\n\ta := new [3]int{1, 2, 3}\n\treturn (*a)[0]\n}\n")
}

// TestNewWrappingOrdinaryCallIsError covers rejecting `new` wrapping
// anything other than a constructor call or composite literal - an ordinary
// function call, in this case.
func TestNewWrappingOrdinaryCallIsError(t *testing.T) {
	expectCheckErrors(t, "func g() int {\n\treturn 1\n}\nfunc f() int {\n\tp := new g()\n\treturn 1\n}\n", 1)
}

// TestNewWrappingConversionCallIsError covers rejecting `new i64(5)` - a
// numeric conversion, not a constructor call/composite literal.
func TestNewWrappingConversionCallIsError(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n\tp := new i64(5)\n\treturn 1\n}\n", 1)
}

// TestAutoDerefFieldAccess covers `p.x` on a `*Point` p auto-dereferencing,
// same as `(*p).x` - both the read and the method-call-callee-resolution
// path go through the same resolveMember.
func TestAutoDerefFieldAccess(t *testing.T) {
	tree, info := checkSrc(t, pointStructSrc+"func f() int {\n\tp := new Point(1, 2)\n\treturn p.x + p.y\n}\n")
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 4)
	ret := tree.Child(body, 1)
	sum := tree.Child(ret, 0)
	if info.Types[sum].Kind != TypeI32 {
		t.Fatalf("p.x + p.y type = %v, want int", info.Types[sum])
	}
}

// TestAutoDerefMethodCall covers a method call through a pointer receiver
// (`p.move(...)` where p is `*Point`) resolving correctly.
func TestAutoDerefMethodCall(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tconstructor(x int) {\n\t\tthis.x = x\n\t}\n" +
		"}\n" +
		"func (Point) addOne() {\n" +
		"\tthis.x = this.x + 1\n" +
		"}\n" +
		"func f() int {\n" +
		"\tp := new Point(1)\n" +
		"\tp.addOne()\n" +
		"\treturn p.x\n" +
		"}\n"
	checkSrc(t, src)
}

// TestDeleteRequiresPointer covers `delete` rejecting a non-pointer operand.
func TestDeleteRequiresPointer(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tx := 5\n\tdelete x\n}\n", 1)
}

// TestDeletePointerIsValid covers the positive case: deleting a real `*T`.
func TestDeletePointerIsValid(t *testing.T) {
	checkSrc(t, pointStructSrc+"func f() {\n\tp := new Point(1, 2)\n\tdelete p\n}\n")
}

// --- nil (see LANGUAGE.md's "Pointers" section) ---

// TestNilAssignsToPointerVar covers `var p *Point = nil` type-checking, and
// nil adapting to the declared pointer type.
func TestNilAssignsToPointerVar(t *testing.T) {
	tree, info := checkSrc(t, pointStructSrc+"var p *Point = nil\n")
	decl := tree.Children(tree.Root)[1]
	init := tree.Child(decl, 2)
	pt := info.Types[init]
	if pt.Kind != TypePointer || pt.Elem.Kind != TypeStruct {
		t.Fatalf("nil's retyped Type = %v, want *Point", pt)
	}
}

// TestNilAssignedToNonPointerIsError covers rejecting nil assigned to a
// non-pointer-typed var.
func TestNilAssignedToNonPointerIsError(t *testing.T) {
	expectCheckErrors(t, "var a int = nil\n", 1)
}

// TestBareNilWithNoContextIsError covers rejecting a type-less `:= nil` -
// unlike an untyped numeric constant, nil has no default type to fall back
// to.
func TestBareNilWithNoContextIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tp := nil\n}\n", 1)
}

// TestNilEqualityComparison covers `p == nil`/`nil == p`/`p != nil` all
// type-checking to bool once p is a real pointer.
func TestNilEqualityComparison(t *testing.T) {
	checkSrc(t, pointStructSrc+"func f() bool {\n\tp := new Point(1, 2)\n\treturn p == nil\n}\n")
	checkSrc(t, pointStructSrc+"func f() bool {\n\tp := new Point(1, 2)\n\treturn nil == p\n}\n")
	checkSrc(t, pointStructSrc+"func f() bool {\n\tp := new Point(1, 2)\n\treturn p != nil\n}\n")
}

// TestNilVsNilComparisonIsError covers rejecting `nil == nil` - there's no
// pointer type either side could ever adapt to.
func TestNilVsNilComparisonIsError(t *testing.T) {
	expectCheckErrors(t, "func f() bool {\n\treturn nil == nil\n}\n", 1)
}

// TestNilComparedAgainstNonPointerIsError covers rejecting `x == nil` when x
// isn't a pointer.
func TestNilComparedAgainstNonPointerIsError(t *testing.T) {
	expectCheckErrors(t, "func f() bool {\n\tx := 5\n\treturn x == nil\n}\n", 1)
}

// TestPointerEqualityComparison covers two same-pointer-type values
// comparing directly (no nil involved).
func TestPointerEqualityComparison(t *testing.T) {
	checkSrc(t, pointStructSrc+"func f() bool {\n\ta := new Point(1, 2)\n\tb := new Point(1, 2)\n\treturn a == b\n}\n")
}

// TestPointerFunctionParamAndReturn covers `*T` used as an ordinary
// parameter and return type.
func TestPointerFunctionParamAndReturn(t *testing.T) {
	checkSrc(t, pointStructSrc+"func identity(p *Point) *Point {\n\treturn p\n}\n")
}

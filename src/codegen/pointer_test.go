package codegen

import "testing"

// TestAddressOfAndDereferenceRoundTrip covers `&x` producing a real pointer
// and `*p` reading/writing back through it (see LANGUAGE.md's "Pointers"
// section) - the most basic address-of/dereference round trip.
func TestAddressOfAndDereferenceRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	x := 5
	p := &x
	*p = 10
	return x
}
`)
	if got := jm.runInt32(t, "f"); got != 10 {
		t.Errorf("f() = %d, want 10 (write through *p should mutate x)", got)
	}
}

// TestDereferenceReadsCurrentValue covers reading `*p` reflecting a later
// mutation of the pointee through the original variable, not a stale copy -
// proof `&x` is a real address, not a spilled snapshot.
func TestDereferenceReadsCurrentValue(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	x := 1
	p := &x
	x = 42
	return *p
}
`)
	if got := jm.runInt32(t, "f"); got != 42 {
		t.Errorf("f() = %d, want 42", got)
	}
}

// TestNewConstructorCallHeapAllocates covers `new Point(x, y)` - a
// constructor call routed through a real malloc'd address (genNewExpr) - and
// that its fields read back correctly through the resulting pointer.
func TestNewConstructorCallHeapAllocates(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func f() int {
	p := new Point(3, 4)
	return p.x + p.y
}
`)
	if got := jm.runInt32(t, "f"); got != 7 {
		t.Errorf("f() = %d, want 7", got)
	}
}

// TestNewCompositeLitHeapAllocates covers `new Point{x, y}` - the
// composite-literal form of `new`, reusing genCompositeLitInto against the
// malloc'd address instead of a constructor call.
func TestNewCompositeLitHeapAllocates(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

func f() int {
	p := new Point{5, 6}
	return p.x + p.y
}
`)
	if got := jm.runInt32(t, "f"); got != 11 {
		t.Errorf("f() = %d, want 11", got)
	}
}

// TestNewStructPersistsAcrossCalls is the real proof `new` heap-allocates
// rather than merely returning a disguised stack address: main constructs a
// value via a helper function and returns its address as a plain integer
// (through i64 - a pointer round-tripped as bits), then a second call reads
// the same address back through *p and confirms the fields are still
// intact - a stack-allocated address wouldn't reliably survive the first
// function call returning.
func TestNewStructPersistsAcrossCalls(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

var stash *Point

func makeAndStash() int {
	stash = new Point(11, 22)
	return stash.x + stash.y
}

func readBack() int {
	return stash.x + stash.y
}
`)
	if got := jm.runInt32(t, "makeAndStash"); got != 33 {
		t.Errorf("makeAndStash() = %d, want 33", got)
	}
	if got := jm.runInt32(t, "readBack"); got != 33 {
		t.Errorf("readBack() = %d, want 33 (heap value must survive)", got)
	}
}

// TestDeleteDoesNotCrash covers `delete p` actually calling through to libc
// `free` without crashing the process - there's no way to observe a freed
// allocation's contents afterward (that would be a real use-after-free), so
// this is a "runs to completion" test, the same shape TestPrintDoesNotCrash
// (string_test.go) already uses for a similar no-crash-only guarantee.
func TestDeleteDoesNotCrash(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func f() int {
	p := new Point(1, 2)
	sum := p.x + p.y
	delete p
	return sum
}
`)
	if got := jm.runInt32(t, "f"); got != 3 {
		t.Errorf("f() = %d, want 3", got)
	}
}

// TestAutoDerefFieldAccessAndMutation covers `p.field` auto-dereferencing a
// `*Point` for both a read and a write (`p.x = ...`), matching `(*p).x`.
func TestAutoDerefFieldAccessAndMutation(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func f() int {
	p := new Point(1, 2)
	p.x = 100
	return p.x + p.y
}
`)
	if got := jm.runInt32(t, "f"); got != 102 {
		t.Errorf("f() = %d, want 102", got)
	}
}

// TestAutoDerefMethodCallMutatesThroughPointer covers a method call through
// a pointer receiver (`p.move(...)` where p is `*Point`) actually mutating
// the shared heap allocation - proof the receiver address passed to the
// method is the real pointee address, not a copy.
func TestAutoDerefMethodCallMutatesThroughPointer(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func (Point) move(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}

func f() int {
	p := new Point(1, 2)
	p.move(10, 20)
	return p.x + p.y
}
`)
	if got := jm.runInt32(t, "f"); got != 33 {
		t.Errorf("f() = %d, want 33 (1+10 + 2+20)", got)
	}
}

// TestPointerParamAndReturn covers `*T` used as an ordinary function
// parameter and return type, mutating the pointee through a function
// boundary.
func TestPointerParamAndReturn(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func setX(p *Point, v int) *Point {
	p.x = v
	return p
}

func f() int {
	p := new Point(1, 2)
	q := setX(p, 99)
	return p.x + q.x
}
`)
	if got := jm.runInt32(t, "f"); got != 198 {
		t.Errorf("f() = %d, want 198 (both p and q see the same mutation)", got)
	}
}

// TestNilPointerComparison covers `nil` lowering to a real null pointer
// constant, comparing equal/not-equal correctly both before and after a
// pointer variable is assigned a real address.
func TestNilPointerComparison(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
}

func isNil() bool {
	var p *Point = nil
	return p == nil
}

func isNotNilAfterNew() bool {
	var p *Point = nil
	p = new Point{1}
	return p != nil
}
`)
	if got := jm.runBool(t, "isNil"); !got {
		t.Errorf("isNil() = %v, want true", got)
	}
	if got := jm.runBool(t, "isNotNilAfterNew"); !got {
		t.Errorf("isNotNilAfterNew() = %v, want true", got)
	}
}

// TestPrintThroughAutoDerefPointer covers `print` on a value read through a
// pointer's auto-derefed field access, exercising the real captured-stdout
// path (see stdout_capture_test.go) rather than only asserting a return
// value.
func TestPrintThroughAutoDerefPointer(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func main() {
	p := new Point(7, 8)
	print(p.x)
	print(p.y)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "7\n8\n"
	if out != want {
		t.Fatalf("print output = %q, want %q", out, want)
	}
}

// TestPointerToPointerRoundTrip covers a real `**int` value end to end - not
// just the parser-level shape assertions TestPointerTypeShape/
// TestAddressOfAndDerefShape (parser/pointer_test.go) already have - proving
// `**pp = v` (double-dereference used as an assignment target) actually
// writes through both indirections to the original variable, and `**pp`
// (used as a value) reads it back correctly.
func TestPointerToPointerRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	x := 5
	p := &x
	pp := &p
	**pp = 99
	return x
}
`)
	if got := jm.runInt32(t, "f"); got != 99 {
		t.Errorf("f() = %d, want 99 (write through **pp should reach x)", got)
	}
}

// TestPointerToPointerReadsCurrentValue covers `**pp` as a value expression
// (not an assignment target) reflecting x's current value through both
// indirections.
func TestPointerToPointerReadsCurrentValue(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	x := 7
	p := &x
	pp := &p
	x = 123
	return **pp
}
`)
	if got := jm.runInt32(t, "f"); got != 123 {
		t.Errorf("f() = %d, want 123", got)
	}
}

// TestStructPointerFieldChainMutation covers a struct with a pointer-typed
// field (self-referential, the `p.next.value` shape LANGUAGE.md's "Pointers"
// section models auto-deref member access on), mutated through the chain
// `head.next.value = ...` and read back through a *different* path (the
// original `tail` pointer the chain was built from) - real proof the write
// landed at the shared heap address, not a copy, not just "it compiles".
func TestStructPointerFieldChainMutation(t *testing.T) {
	jm := compileAndJIT(t, `
struct Node {
	value int
	next *Node
}

func f() int {
	tail := new Node{1, nil}
	head := new Node{0, tail}
	head.next.value = 42
	return tail.value
}
`)
	if got := jm.runInt32(t, "f"); got != 42 {
		t.Errorf("f() = %d, want 42 (write through head.next.value should reach the same node tail points to)", got)
	}
}

// TestDeleteViaStructFieldDoesNotNullField covers `delete` on a pointer read
// out of a struct field (see LANGUAGE.md's "Pointers" section's now-corrected
// delete documentation): `free` is still called correctly (no crash), but
// the narrow null-out mitigation genDeleteStmt applies to a bare local/
// parameter (deleteLocalSlot, stmt.go) does *not* reach into the field's own
// storage - it isn't an Ident at all, so the field is left holding its
// stale (now-freed) address, exactly as documented, not silently nulled.
func TestDeleteViaStructFieldDoesNotNullField(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
}

struct Box {
	p *Point
}

func f() bool {
	var b Box
	b.p = new Point{1}
	delete b.p
	return b.p == nil
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = %v, want false (delete via a struct field must not null the field itself)", got)
	}
}

// TestDeleteViaParameterDoesNotNullCallersCopy covers `delete` on a pointer
// passed into a function as a parameter: the callee's own parameter is a
// bare local (its own alloca - see allocLocalSlot), so `delete param` inside
// the callee does null *that* parameter's own slot (matching item 1's
// documented scope), but the caller passed a copy of the pointer value, not
// a shared address for that variable - the caller's own variable has its own
// independent storage, so it is correctly left completely unaffected. This
// is the "second variable/parameter holding a copy of the same address"
// non-case LANGUAGE.md's "Pointers" section now documents explicitly.
func TestDeleteViaParameterDoesNotNullCallersCopy(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
}

func consume(p *Point) {
	delete p
}

func f() bool {
	q := new Point{1}
	consume(q)
	return q == nil
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = %v, want false (the caller's own copy of the pointer must not be nulled by the callee's delete)", got)
	}
}

// TestDeleteNullsOutLocalVariable is the real hands-on-verifiable proof of
// item 1's fix itself: `delete p` on a bare local sets p's own slot to a
// real null pointer afterward, so `p == nil` reads true immediately - the
// mitigation LANGUAGE.md's "Pointers" section now documents turns a
// same-variable use-after-free into a clean, deterministic nil-pointer
// trap instead of silent corruption.
func TestDeleteNullsOutLocalVariable(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
}

func f() bool {
	p := new Point{1}
	delete p
	return p == nil
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (delete on a bare local must null its own slot)", got)
	}
}

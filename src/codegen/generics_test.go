package codegen

import (
	"strings"
	"testing"
)

// TestGenericFuncTwoInstantiationsProduceIndependentResults is this round's
// non-negotiable regression test: one generic function called with two
// different concrete type arguments in the same program must lower to two
// genuinely separate functions, each computing its own type's arithmetic. A
// lookup that ignored the instantiation and reused whichever specialization
// was generated last would be silently WRONG here, not a crash.
func TestGenericFuncTwoInstantiationsProduceIndependentResults(t *testing.T) {
	jm := compileAndJIT(t, `
func Sum[T](a T, b T) T {
	return a + b
}

func main() {
	print(Sum(1, 2))
	print(Sum(1.5, 2.25))
	print(Sum("a", "b"))
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "3\n3.750000\nab\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

// The struct counterpart to the test above: two instantiations of one generic
// struct, each with its own layout and its own copy of the same method.
func TestGenericStructTwoInstantiationsProduceIndependentResults(t *testing.T) {
	jm := compileAndJIT(t, `
struct Box[T] {
	value T
}

func (Box[T]) Get() T {
	return this.value
}

func (Box[T]) Set(v T) {
	this.value = v
}

func main() {
	a := Box[int]{7}
	b := Box[f64]{1.5}
	a.Set(9)
	print(a.Get())
	print(b.Get())
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "9\n1.500000\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

func TestGenericFuncMultipleTypeParamsLower(t *testing.T) {
	jm := compileAndJIT(t, `
struct Pair[A, B] {
	first  A
	second B
}

func MakePair[A, B](a A, b B) Pair[A, B] {
	return Pair[A, B]{a, b}
}

func main() {
	p := MakePair(3, "hi")
	print(p.first)
	print(p.second)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "3\nhi\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

func TestGenericMethodWithItsOwnTypeParamLowers(t *testing.T) {
	jm := compileAndJIT(t, `
struct Entity {
	id int
}

func (Entity) Tag[T](value T) T {
	return value
}

func main() {
	e := Entity{5}
	print(e.Tag(7))
	print(e.Tag("x"))
	print(e.id)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "7\nx\n5\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

func TestGenericExplicitInstantiationLowers(t *testing.T) {
	jm := compileAndJIT(t, `
func Zeros[T](n int) []T {
	return make([]T, n)
}

func main() {
	a := Zeros[int](3)
	print(len(a))
	print(a[0])
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "3\n0\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

// Every specialization gets its own LLVM function, named after the mangled
// specialization rather than the shared template - two same-named functions
// would otherwise be indistinguishable in the IR.
func TestSpecializationsGetDistinctLLVMNames(t *testing.T) {
	jm := compileAndJIT(t, `
func Id[T](a T) T {
	return a
}

func main() {
	print(Id(1))
	print(Id(1.5))
}
`)
	for _, want := range []string{"@\"Id[int]\"", "@\"Id[f64]\""} {
		if !strings.Contains(jm.ir, want) {
			t.Fatalf("generated IR has no %s function:\n%s", want, jm.ir)
		}
	}
}

// A generic struct's own constructor is cloned and lowered per instantiation
// exactly like its methods are - `Box[int](7)` is an ordinary constructor
// call on an ordinary concrete struct by the time codegen sees it.
func TestGenericStructConstructorLowers(t *testing.T) {
	jm := compileAndJIT(t, `
struct Box[T] {
	value T
	constructor(v T) {
		this.value = v
	}
}

func main() {
	a := Box[int](7)
	b := Box[f64](1.5)
	print(a.value)
	print(b.value)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "7\n1.500000\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

// TestGenericSlotMap is this feature's motivating end-to-end case: a
// generational-handle slot map, instantiated with two different concrete
// element types in one program (see LANGUAGE.md's "Generics" section).
func TestGenericSlotMap(t *testing.T) {
	jm := compileAndJIT(t, slotMapProgram)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "10\n20\n1\n2\n30\n7\n8\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
}

// slotMapProgram is shared with cmd/llvmc's own end-to-end run of the same
// source through the real CLI (see cmd/llvmc/generics_test.go).
const slotMapProgram = `
struct Entity {
	hp  int
	mp  int
}

struct SlotMap[T] {
	items       []T
	generations []i32
	freeList    []int
}

func (SlotMap[T]) Insert(v T) int {
	n := len(this.freeList)
	if n > 0 {
		slot := this.freeList[n-1]
		this.freeList = this.freeList[0:n-1]
		this.items[slot] = v
		return slot
	}
	this.items = append(this.items, v)
	this.generations = append(this.generations, 0)
	return len(this.items) - 1
}

func (SlotMap[T]) Get(i int) T {
	return this.items[i]
}

func (SlotMap[T]) Remove(i int) {
	this.generations[i] = this.generations[i] + 1
	this.freeList = append(this.freeList, i)
}

func (SlotMap[T]) Generation(i int) i32 {
	return this.generations[i]
}

func NewSlotMap[T]() SlotMap[T] {
	return SlotMap[T]{make([]T, 0), make([]i32, 0), make([]int, 0)}
}

func main() {
	ints := NewSlotMap[int]()
	a := ints.Insert(10)
	b := ints.Insert(20)
	print(ints.Get(a))
	print(ints.Get(b))

	ints.Remove(a)
	print(ints.Generation(a))
	reused := ints.Insert(30)
	ints.Remove(reused)
	print(ints.Generation(reused))
	ints.Insert(30)
	print(ints.Get(reused))

	entities := NewSlotMap[Entity]()
	e := entities.Insert(Entity{7, 8})
	print(entities.Get(e).hp)
	print(entities.Get(e).mp)
}
`

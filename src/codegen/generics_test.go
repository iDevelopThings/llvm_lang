package codegen

import (
	"strings"
	"testing"

	"llvm_lang/src/sema"
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

// One generic calling another, with the type parameter flowing through: the
// outer specialization's own body triggers the inner instantiation.
func TestGenericCallingGenericLowers(t *testing.T) {
	jm := compileAndJIT(t, `
func Inner[T](x T) T {
	return x
}

func Outer[T](x T) T {
	return Inner(x)
}

func main() {
	print(Outer(4))
	print(Outer("z"))
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	if want := "4\nz\n"; out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
	}
	for _, name := range []string{`@"Outer[int]"`, `@"Inner[int]"`, `@"Outer[string]"`, `@"Inner[string]"`} {
		if !hasDefine(jm.ir, name) {
			t.Fatalf("generated IR defines no %s:\n%s", name, jm.ir)
		}
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
// specialization rather than the shared template - and the template itself is
// never emitted at all.
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
	for _, want := range []string{`@"Id[int]"`, `@"Id[f64]"`} {
		if !hasDefine(jm.ir, want) {
			t.Fatalf("generated IR defines no %s:\n%s", want, jm.ir)
		}
	}
	if strings.Contains(jm.ir, "@Id(") {
		t.Fatalf("generated IR mentions the un-substituted template @Id:\n%s", jm.ir)
	}
}

// hasDefine reports whether ir carries a real body for the LLVM symbol name -
// a mere call site mentioning it doesn't count.
func hasDefine(ir, name string) bool {
	for line := range strings.Lines(ir) {
		if strings.HasPrefix(line, "define") && strings.Contains(line, name+"(") {
			return true
		}
	}
	return false
}

// Two same-named structs from different packages render identically via
// Type.String(), so one shared generic instantiated at both produces the same
// mangled name twice - sema's instanceKey disambiguates with a `#N` suffix.
// Without that, the second instantiation would silently alias onto the first
// and compute the wrong answer (not crash).
func TestSameNamedTypeArgsFromDifferentPackages(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "gen",
			files: []packageFile{
				{"gen/total.llx", `
func Total[T](v T) int {
	return v.Sum()
}
`},
			},
		},
		{
			key: "a",
			files: []packageFile{
				{"a/point.llx", `
struct Point {
	X int
}

func (Point) Sum() int {
	return this.X
}
`},
			},
		},
		{
			key: "b",
			files: []packageFile{
				{"b/point.llx", `
struct Point {
	X int
	Y int
}

func (Point) Sum() int {
	return this.X + this.Y
}
`},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
import "./gen"
import "./a"
import "./b"

func main() {
	print(gen.Total(a.Point{10}))
	print(gen.Total(b.Point{10, 5}))
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {
					{LocalName: "gen", TargetKey: "gen"},
					{LocalName: "a", TargetKey: "a"},
					{LocalName: "b", TargetKey: "b"},
				},
			},
		},
	})

	for _, want := range []string{`@"Total[Point]"`, `@"Total[Point]#1"`} {
		if !hasDefine(jm.ir, want) {
			t.Fatalf("generated IR defines no %s:\n%s", want, jm.ir)
		}
	}

	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	if want := "10\n15\n"; out != want {
		t.Fatalf("captured stdout = %q, want %q", out, want)
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

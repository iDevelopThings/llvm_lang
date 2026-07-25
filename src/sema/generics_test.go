package sema

import (
	"strings"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// --- valid paths ---

func TestGenericFuncInferredFromArguments(t *testing.T) {
	checkSrc(t, "func Sum[T](a T, b T) T {\n\treturn a + b\n}\n"+
		"func main() {\n\tprint(Sum(1, 2))\n}\n")
}

func TestGenericFuncTwoInstantiationsCoexist(t *testing.T) {
	tree, info := checkSrc(t, "func Sum[T](a T, b T) T {\n\treturn a + b\n}\n"+
		"func main() {\n\tprint(Sum(1, 2))\n\tprint(Sum(1.5, 2.5))\n}\n")
	wantSpecializations(t, tree, info, "Sum[int]", "Sum[f64]")
}

func TestGenericFuncMultipleTypeParams(t *testing.T) {
	tree, info := checkSrc(t, "func First[A, B](a A, b B) A {\n\treturn a\n}\n"+
		"func main() {\n\tprint(First(1, \"x\"))\n}\n")
	wantSpecializations(t, tree, info, "First[int,string]")
}

func TestGenericFuncExplicitInstantiation(t *testing.T) {
	tree, info := checkSrc(t, "func Zero[T](a T) T {\n\treturn a\n}\n"+
		"func main() {\n\tprint(Zero[i64](5))\n}\n")
	wantSpecializations(t, tree, info, "Zero[i64]")
}

// A type parameter reachable only through the return type can't be inferred,
// but explicit instantiation still works - the escape hatch's whole point.
func TestGenericFuncReturnOnlyTypeParamExplicit(t *testing.T) {
	tree, info := checkSrc(t, "func Make[T](n int) []T {\n\treturn make([]T, n)\n}\n"+
		"func main() {\n\tvar a []int = Make[int](3)\n\tprint(len(a))\n}\n")
	wantSpecializations(t, tree, info, "Make[int]")
}

func TestGenericFuncInferredThroughSliceParam(t *testing.T) {
	checkSrc(t, "func Head[T](items []T) T {\n\treturn items[0]\n}\n"+
		"func main() {\n\ts := make([]int, 1)\n\tprint(Head(s))\n}\n")
}

func TestGenericStructConstructionAndMethods(t *testing.T) {
	tree, info := checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Get() T {\n\treturn this.value\n}\n"+
		"func main() {\n\tb := Box[int]{7}\n\tprint(b.Get())\n}\n")
	wantSpecializations(t, tree, info, "Box[int]", "Get")
	if _, ok := info.Structs["Box[int]"]; !ok {
		t.Fatalf("no Box[int] in the struct catalog: %v", structNames(info))
	}
}

func TestGenericStructTwoInstantiationsCoexist(t *testing.T) {
	_, info := checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Get() T {\n\treturn this.value\n}\n"+
		"func main() {\n\ta := Box[int]{7}\n\tb := Box[f64]{1.5}\n\tprint(a.Get())\n\tprint(b.Get())\n}\n")
	for _, want := range []string{"Box[int]", "Box[f64]"} {
		if _, ok := info.Structs[want]; !ok {
			t.Fatalf("no %s in the struct catalog: %v", want, structNames(info))
		}
	}
	if info.Structs["Box[int]"].Methods["Get"] == info.Structs["Box[f64]"].Methods["Get"] {
		t.Fatal("Box[int].Get and Box[f64].Get resolved to the same symbol")
	}
}

// The receiver clause may spell the struct's type parameters differently -
// they're positional, not matched by name.
func TestGenericStructReceiverRenamesTypeParam(t *testing.T) {
	checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[E]) Get() E {\n\treturn this.value\n}\n"+
		"func main() {\n\tb := Box[int]{7}\n\tprint(b.Get())\n}\n")
}

func TestGenericMethodOnNonGenericStruct(t *testing.T) {
	checkSrc(t, "struct Entity {\n\tid int\n}\n"+
		"func (Entity) SameAs[T](other T) bool {\n\treturn true\n}\n"+
		"func main() {\n\te := Entity{1}\n\tprint(e.SameAs(5))\n}\n")
}

func TestGenericMethodOwnTypeParamOnGenericStruct(t *testing.T) {
	checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Pair[U](other U) T {\n\treturn this.value\n}\n"+
		"func main() {\n\tb := Box[int]{7}\n\tprint(b.Pair(\"x\"))\n}\n")
}

// A generic struct used as a generic function's parameter type: T is inferred
// through the already-instantiated argument's own recorded type arguments.
func TestInferenceThroughGenericStructParam(t *testing.T) {
	checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func Unwrap[T](b Box[T]) T {\n\treturn b.value\n}\n"+
		"func main() {\n\tb := Box[int]{7}\n\tprint(Unwrap(b))\n}\n")
}

func TestGenericFuncCallingAnotherGenericFunc(t *testing.T) {
	tree, info := checkSrc(t, "func Id[T](a T) T {\n\treturn a\n}\n"+
		"func Twice[T](a T) T {\n\treturn Id(Id(a))\n}\n"+
		"func main() {\n\tprint(Twice(3))\n}\n")
	wantSpecializations(t, tree, info, "Twice[int]", "Id[int]")
}

// A generic that calls itself at the SAME type must terminate: the (template,
// type arguments) instance is registered before its body is ever resolved, so
// the recursive call is a cache hit rather than a fresh instantiation.
func TestSelfRecursiveGenericTerminates(t *testing.T) {
	tree, info := checkSrc(t, "func Countdown[T](a T, n int) T {\n\tif n <= 0 {\n\t\treturn a\n\t}\n\treturn Countdown(a, n-1)\n}\n"+
		"func main() {\n\tprint(Countdown(1, 3))\n}\n")
	wantSpecializations(t, tree, info, "Countdown[int]")
	if got := len(info.Specializations); got != 1 {
		t.Fatalf("created %d specializations, want exactly 1", got)
	}
}

func TestMutuallyRecursiveGenericsTerminate(t *testing.T) {
	tree, info := checkSrc(t, "func Ping[T](a T, n int) T {\n\tif n <= 0 {\n\t\treturn a\n\t}\n\treturn Pong(a, n-1)\n}\n"+
		"func Pong[T](a T, n int) T {\n\treturn Ping(a, n-1)\n}\n"+
		"func main() {\n\tprint(Ping(1, 3))\n}\n")
	wantSpecializations(t, tree, info, "Ping[int]", "Pong[int]")
	if got := len(info.Specializations); got != 2 {
		t.Fatalf("created %d specializations, want exactly 2", got)
	}
}

// A generic struct whose own method recurses on the same instantiation - the
// method-level counterpart to the two cases above.
func TestRecursiveGenericMethodTerminates(t *testing.T) {
	checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Down(n int) T {\n\tif n <= 0 {\n\t\treturn this.value\n\t}\n\treturn this.Down(n-1)\n}\n"+
		"func main() {\n\tb := Box[int]{7}\n\tprint(b.Down(3))\n}\n")
}

// A generic declared in one package and instantiated from another: the
// specialization belongs to the DECLARING package's own tree/Info, since
// that's where the declaration it clones lives.
func TestGenericInstantiatedAcrossPackages(t *testing.T) {
	libTree := mustParseFile(t, "lib/box.llx", "struct Box[T] {\n\tValue T\n}\n"+
		"func (Box[T]) Get() T {\n\treturn this.Value\n}\n"+
		"func Identity[T](a T) T {\n\treturn a\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./lib\"\n\n"+
		"func main() {\n\tb := lib.Box[int]{7}\n\tprint(b.Get())\n\tprint(lib.Identity(3))\n}\n")

	units := []*PackageUnit{
		{Key: "lib", Name: "lib", Trees: []*ast.Tree{libTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "lib", TargetKey: "lib"}},
			},
		},
	}
	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// A specialization's body belongs to the package that DECLARED the generic,
// not to whichever package's call site triggered it - so it may still touch
// its own package's unexported names.
func TestGenericSpecializationKeepsDeclaringPackageIdentity(t *testing.T) {
	libTree := mustParseFile(t, "lib/box.llx", "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Get() T {\n\treturn this.value\n}\n"+
		"func Wrap[T](v T) Box[T] {\n\treturn Box[T]{v}\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./lib\"\n\n"+
		"func main() {\n\tb := lib.Wrap(7)\n\tprint(b.Get())\n}\n")

	units := []*PackageUnit{
		{Key: "lib", Name: "lib", Trees: []*ast.Tree{libTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "lib", TargetKey: "lib"}},
			},
		},
	}
	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// --- invalid paths ---

// A generic instantiating itself at a strictly larger type has no finite fixed
// point - it must fail loudly rather than never returning.
func TestUnboundedGenericRecursionIsRejected(t *testing.T) {
	_, _, diags := checkSrcAllowErrors(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func Grow[T](a T) int {\n\treturn Grow(Box[T]{a})\n}\n"+
		"func main() {\n\tprint(Grow(1))\n}\n")
	if !diags.HasErrors() {
		t.Fatal("expected a diagnostic for unbounded generic recursion")
	}
	wantDiagAmong(t, diags.All(), "too many generic instantiations")
}

func TestGenericInconsistentUnificationRejected(t *testing.T) {
	diags := expectCheckErrors(t, "func Pair[T](a T, b T) T {\n\treturn a\n}\n"+
		"func main() {\n\tprint(Pair(1, \"x\"))\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "cannot infer type parameter T")
}

func TestGenericUninferableTypeParamRejected(t *testing.T) {
	diags := expectCheckErrors(t, "func Make[T](n int) []T {\n\treturn make([]T, n)\n}\n"+
		"func main() {\n\tMake(3)\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "instantiate it explicitly")
}

// The whole point of checking per instantiation: the body is only rejected for
// the concrete T that can't support it.
func TestGenericBodyRejectedForUnsupportedInstantiation(t *testing.T) {
	diags := expectCheckErrors(t, "struct P {\n\tx int\n}\n"+
		"func Sum[T](a T, b T) T {\n\treturn a + b\n}\n"+
		"func main() {\n\tp := P{1}\n\tSum(p, p)\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "operator + not defined for P and P")
}

func TestGenericWrongTypeArgumentCountRejected(t *testing.T) {
	diags := expectCheckErrors(t, "func Id[T](a T) T {\n\treturn a\n}\n"+
		"func main() {\n\tprint(Id[int, bool](1))\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "takes 1 type argument")
}

func TestGenericStructWithoutTypeArgumentsRejected(t *testing.T) {
	diags := expectCheckErrors(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func main() {\n\tvar b Box\n\tprint(1)\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "is generic - name its type arguments")
}

func TestGenericFuncAsValueRejected(t *testing.T) {
	diags := expectCheckErrors(t, "func Id[T](a T) T {\n\treturn a\n}\n"+
		"func main() {\n\tf := Id\n\tprint(1)\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "is generic and cannot be used as a value")
}

func TestGenericStructWrongTypeArgCountRejected(t *testing.T) {
	diags := expectCheckErrors(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func main() {\n\tb := Box[int, bool]{1}\n\tprint(1)\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "takes 1 type argument")
}

// A method's own type parameter shadowing one its receiver already binds is
// rejected outright - see generics.go's typeParamNames.
func TestMethodTypeParamCollidingWithReceiverRejected(t *testing.T) {
	diags := expectResolveErrors(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Get[T](other T) T {\n\treturn this.value\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "already bound by the receiver clause")
}

func TestDuplicateTypeParamNameRejected(t *testing.T) {
	diags := expectResolveErrors(t, "func Bad[T, T](a T) T {\n\treturn a\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "type parameter T redeclared")
}

func TestReceiverTypeParamsOnNonGenericStructRejected(t *testing.T) {
	diags := expectResolveErrors(t, "struct P {\n\tx int\n}\n"+
		"func (P[T]) Get() int {\n\treturn this.x\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "is not a generic type")
}

func TestGenericStructReceiverArityMismatchRejected(t *testing.T) {
	diags := expectResolveErrors(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[A, B]) Get() int {\n\treturn 1\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "its receiver clause must name exactly that many")
}

func TestGenericInstantiationAsValueRejected(t *testing.T) {
	diags := expectCheckErrors(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func main() {\n\tb := Box[int]\n\tprint(1)\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "is not a value here")
}

// Explicit type arguments on a METHOD call have no supported spelling (see
// BLOCKERS.md) - what matters here is that it's a clean diagnostic, not a
// crash or something silently surprising.
func TestExplicitTypeArgsOnMethodCallRejected(t *testing.T) {
	_, _, diags := checkSrcAllowErrors(t, "struct Entity {\n\tid int\n}\n"+
		"func (Entity) Tag[T](v T) T {\n\treturn v\n}\n"+
		"func main() {\n\te := Entity{1}\n\tprint(e.Tag[int](5))\n}\n")
	if !diags.HasErrors() {
		t.Fatal("expected a diagnostic for explicit type arguments on a method call")
	}
}

// Ordinary indexing must be completely unaffected by the `Foo[T]` grammar
// overlap - the target simply isn't generic.
func TestOrdinaryIndexingStillWorks(t *testing.T) {
	checkSrc(t, "func main() {\n\ta := make([]int, 3)\n\tprint(a[0])\n}\n")
}

// Map indexing shares the same grammar too, and a map-typed local is the
// likeliest thing to be mistaken for an instantiation.
func TestMapIndexingStillWorks(t *testing.T) {
	checkSrc(t, "func main() {\n\tm := make(map[string]int)\n\tm[\"a\"] = 1\n\tprint(m[\"a\"])\n}\n")
}

// A generic struct's own constructor/destructor are cloned and checked per
// instantiation exactly like its fields and methods are.
func TestGenericStructConstructorAndDestructor(t *testing.T) {
	checkSrc(t, "struct Box[T] {\n\tvalue T\n\tconstructor(v T) {\n\t\tthis.value = v\n\t}\n\tdestructor() {\n\t\tprint(1)\n\t}\n}\n"+
		"func main() {\n\tb := Box[int](7)\n\tprint(b.value)\n}\n")
}

// A pointer type argument goes through the expression-position `*T` shape the
// Pratt parser produces (a unary deref), reinterpreted as a type only in the
// type-argument position - see the parser's atTypeOnlyStart.
func TestPointerTypeArgument(t *testing.T) {
	tree, info := checkSrc(t, "struct Entity {\n\tid int\n}\n"+
		"func Same[T](v T) T {\n\treturn v\n}\n"+
		"func main() {\n\te := Entity{1}\n\tprint(Same[*Entity](&e).id)\n}\n")
	wantSpecializations(t, tree, info, "Same[*Entity]")
}

// --- helpers ---

// wantSpecializations asserts every named specialization was actually
// synthesized - the mangled name for a func/struct, the plain method name for
// a generic struct's own method.
func wantSpecializations(t *testing.T, tree *ast.Tree, info *Info, names ...string) {
	t.Helper()
	got := make(map[string]bool, len(info.Specializations))
	for _, d := range info.Specializations {
		nameNode := tree.Child(d, 0)
		if tree.Nodes[d].Kind == enums.NodeKinds.FuncDecl {
			nameNode = tree.FuncName(d)
		}
		if sym, ok := info.Refs[nameNode]; ok {
			got[sym.Name] = true
		}
	}
	for _, want := range names {
		if !got[want] {
			t.Fatalf("no specialization named %q; got %v", want, got)
		}
	}
}

func structNames(info *Info) []string {
	names := make([]string, 0, len(info.Structs))
	for name := range info.Structs {
		names = append(names, name)
	}
	return names
}

func wantDiagAmong(t *testing.T, got []diag.Diagnostic, want string) {
	t.Helper()
	for _, d := range got {
		if strings.Contains(d.Msg, want) {
			return
		}
	}
	t.Fatalf("no diagnostic mentions %q: %v", want, got)
}

func wantDiag(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("diagnostic %q does not mention %q", got, want)
	}
}

func expectResolveErrors(t *testing.T, src string, want int) *diag.Bag {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	_, rdiags := Resolve(tree)
	if rdiags.ErrorCount() != want {
		t.Fatalf("resolve ErrorCount = %d, want %d: %v", rdiags.ErrorCount(), want, rdiags.All())
	}
	return rdiags
}

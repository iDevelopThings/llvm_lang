package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// resolveSrc parses src and resolves it, failing the test if parsing
// itself produced a diagnostic - a parse error means the test source is
// broken, not the resolver under test.
func resolveSrc(t *testing.T, src string) (*ast.Tree, *Info) {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	info, sdiags := Resolve(tree)
	if sdiags.HasErrors() {
		t.Fatalf("unexpected resolve errors for %q: %v", src, sdiags.All())
	}
	return tree, info
}

func TestBasicVarResolves(t *testing.T) {
	tree, info := resolveSrc(t, "var a int = 5\n")
	decl := tree.Children(tree.Root)[0]
	name := tree.Child(decl, 0)
	sym, ok := info.Refs[name]
	if !ok {
		t.Fatal("expected a Ref for the var's name")
	}
	if sym.Kind != SymVar || sym.Name != "a" {
		t.Errorf("sym = %+v, want Kind=SymVar Name=a", sym)
	}
}

func TestUndefinedVariable(t *testing.T) {
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", "var a = b\n"))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, sdiags := Resolve(tree)
	if sdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", sdiags.ErrorCount(), sdiags.All())
	}
}

func TestForwardReferenceBetweenTopLevelFuncs(t *testing.T) {
	// Go allows package-level declarations to reference each other
	// regardless of order - `a` calls `b`, declared later in the file.
	resolveSrc(t, "func a() { b() }\nfunc b() { }\n")
}

func TestBlockShadowingIsNotRedeclaration(t *testing.T) {
	// A different scope, so this must NOT report "x redeclared".
	resolveSrc(t, "func f() {\n\tvar x int = 1\n\t{\n\t\tvar x int = 2\n\t}\n}\n")
}

func TestRedeclarationInSameScope(t *testing.T) {
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", "func f() {\n\tvar x int = 1\n\tvar x int = 2\n}\n"))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, sdiags := Resolve(tree)
	if sdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", sdiags.ErrorCount(), sdiags.All())
	}
}

func TestShortVarDeclSeesOuterNameOnRHS(t *testing.T) {
	// `x := x + 1` inside f must resolve the right-hand x to the
	// package-level one, not the new local x it's about to declare -
	// Go's actual rule: a variable's scope begins after its declaration.
	src := "var x int = 1\nfunc f() {\n\tx := x + 1\n\t_ = x\n}\n"
	// `_ = x` isn't real syntax we support (no blank identifier) - drop it
	// and just check resolution succeeds with the right symbol below.
	src = "var x int = 1\nfunc f() {\n\tx := x + 1\n}\n"
	tree, info := resolveSrc(t, src)
	funcDecl := tree.Children(tree.Root)[1]
	body := tree.Child(funcDecl, 4)
	shortDecl := tree.Child(body, 0)
	rhs := tree.Child(shortDecl, 1) // `x + 1`
	rhsX := tree.Child(rhs, 0)      // the `x` in `x + 1`

	outerX := info.Refs[tree.Child(tree.Children(tree.Root)[0], 0)]
	gotSym := info.Refs[rhsX]
	if gotSym != outerX {
		t.Fatalf("RHS x resolved to %+v, want the package-level x (%+v)", gotSym, outerX)
	}
}

func TestForLoopVariableScopedToLoop(t *testing.T) {
	tree, info := resolveSrc(t, "func f() {\n\tfor i := 0; i < 10; i++ {\n\t\tprint(i)\n\t}\n}\n")
	funcDecl := tree.Children(tree.Root)[0]
	body := tree.Child(funcDecl, 4)
	forStmt := tree.Child(body, 0)
	forScope := info.Scopes[forStmt]
	if forScope == nil {
		t.Fatal("expected a Scope recorded for the ForStmt")
	}
	if _, ok := forScope.Lookup("i"); !ok {
		t.Fatal("expected i to be visible within the for-loop's own scope")
	}
}

func TestMethodAndThisResolve(t *testing.T) {
	src := "struct Point {\n\tx int\n\ty int\n}\n" +
		"func (Point) move(dx int, dy int) {\n\tthis.x = this.x + dx\n}\n"
	tree, info := resolveSrc(t, src)

	structDecl := tree.Children(tree.Root)[0]
	structInfo := info.Structs["Point"]
	if structInfo == nil {
		t.Fatal("expected StructInfo for Point")
	}
	if len(structInfo.Fields) != 2 {
		t.Errorf("Point has %d cataloged fields, want 2", len(structInfo.Fields))
	}
	if _, ok := structInfo.Methods["move"]; !ok {
		t.Error("expected move to be cataloged as a method on Point")
	}
	_ = structDecl

	methodDecl := tree.Children(tree.Root)[1]
	body := tree.Child(methodDecl, 4)
	assign := tree.Child(body, 0)
	thisExpr := tree.Child(tree.Child(assign, 0), 0) // MemberExpr(x).object = ThisExpr
	sym, ok := info.Refs[thisExpr]
	if !ok || sym.Kind != SymReceiver {
		t.Fatalf("this did not resolve to a SymReceiver symbol: %+v (ok=%v)", sym, ok)
	}
}

func TestThisOutsideMethodIsAnError(t *testing.T) {
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", "func f() {\n\tvar a = this\n}\n"))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, sdiags := Resolve(tree)
	if sdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", sdiags.ErrorCount(), sdiags.All())
	}
}

func TestMethodReceiverMustBeDeclaredStruct(t *testing.T) {
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", "func (Nope) m() { }\n"))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, sdiags := Resolve(tree)
	if sdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", sdiags.ErrorCount(), sdiags.All())
	}
}

func TestRedeclaredTopLevelName(t *testing.T) {
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", "var a int = 1\nvar a int = 2\n"))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, sdiags := Resolve(tree)
	if sdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", sdiags.ErrorCount(), sdiags.All())
	}
}

func TestVariableUsedAsTypeIsRejected(t *testing.T) {
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", "var a int = 1\nvar b a\n"))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, sdiags := Resolve(tree)
	if sdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", sdiags.ErrorCount(), sdiags.All())
	}
}

func TestBuiltinsResolveWithoutError(t *testing.T) {
	resolveSrc(t, "var a int = 1\nvar b string = \"x\"\nvar c bool = true\nfunc f() {\n\tprint(a)\n}\n")
}

func TestCompositeLitKeyNotResolvedValueIs(t *testing.T) {
	src := "struct Point {\n\tx int\n}\n" +
		"func f() {\n\tv := 5\n\tp := Point{x: v}\n}\n"
	tree, info := resolveSrc(t, src)
	funcDecl := tree.Children(tree.Root)[1]
	body := tree.Child(funcDecl, 4)
	pDecl := tree.Child(body, 1) // `p := Point{x: v}`
	lit := tree.Child(pDecl, 1)
	kv := tree.Child(lit, 1) // the `x: v` KeyValueExpr
	key := tree.Child(kv, 0)
	value := tree.Child(kv, 1)

	if _, resolved := info.Refs[key]; resolved {
		t.Error("composite literal key must NOT be resolved by this pass (needs type info)")
	}
	if _, resolved := info.Refs[value]; !resolved {
		t.Error("composite literal value must resolve lexically")
	}
}

func TestArrayTypeElementResolves(t *testing.T) {
	tree, info := resolveSrc(t, "var a [5]int\n")
	decl := tree.Children(tree.Root)[0]
	arrType := tree.Child(decl, 1)
	elem := tree.Child(arrType, 1)
	if _, ok := info.Refs[elem]; !ok {
		t.Error("expected the array type's element type (int) to resolve")
	}
}

func TestFuncScopeOwnerMatchesDeclNode(t *testing.T) {
	// Sanity check for closure-readiness: a function's Scope.Owner must be
	// its own FuncDecl node, so a later capture-analysis pass can compare
	// "which function owns this symbol's scope" against "which function am
	// I currently inside" using nothing but data this pass already builds.
	tree, info := resolveSrc(t, "func f() {\n\tvar x int = 1\n}\n")
	funcDecl := tree.Children(tree.Root)[0]
	scope := info.Scopes[funcDecl]
	if scope == nil || scope.Owner != funcDecl {
		t.Fatalf("Scope.Owner = %v, want %v", scope, funcDecl)
	}
}

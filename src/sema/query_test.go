package sema

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

// TestScopeVisible_ShadowingNearerWins covers Scope.Visible()'s core
// contract: a name declared in a nearer scope must shadow the same name
// declared in an outer one, not yield both.
func TestScopeVisible_ShadowingNearerWins(t *testing.T) {
	tree, info := resolveSrc(t, "var x int = 1\nfunc f() {\n\tvar x int = 2\n\tprint(x)\n}\n")

	funcDecl := tree.Children(tree.Root)[1]
	body := tree.Child(funcDecl, 5)
	printStmt := tree.Child(body, 1)

	scope := info.EnclosingScope(tree, printStmt)
	if scope == nil {
		t.Fatal("expected a non-nil enclosing scope for the print statement")
	}

	innerXDecl := tree.Child(body, 0)
	innerXName := tree.Child(innerXDecl, 0)
	innerSym := info.Refs[innerXName]
	if innerSym == nil {
		t.Fatal("expected a resolved Ref for the inner x's own declaring name")
	}

	var found *Symbol
	count := 0
	for sym := range scope.Visible() {
		if sym.Name == "x" {
			found = sym
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Visible() yielded %d symbols named x, want exactly 1 (the inner one shadowing the outer)", count)
	}
	if found != innerSym {
		t.Error("Visible() yielded the outer package-level x, want the inner (shadowing) one")
	}
}

// TestEnclosingScope_WalksPastNonOwningNodes covers the common case a
// cursor query hits: the node under the cursor (here, an ExprStmt nested
// inside an if-block) has no Info.Scopes entry of its own - only Block/
// FuncDecl/... do - so EnclosingScope must walk up through intervening
// non-scope-owning nodes to find the nearest one that does.
func TestEnclosingScope_WalksPastNonOwningNodes(t *testing.T) {
	tree, info := resolveSrc(t, "func f() {\n\tif true {\n\t\tvar y int = 1\n\t\tprint(y)\n\t}\n}\n")

	funcDecl := tree.Children(tree.Root)[0]
	body := tree.Child(funcDecl, 5)
	ifStmt := tree.Child(body, 0)
	ifBody := tree.Child(ifStmt, 1)
	printStmt := tree.Child(ifBody, 1)

	scope := info.EnclosingScope(tree, printStmt)
	if scope == nil {
		t.Fatal("expected a non-nil enclosing scope")
	}
	if _, ok := scope.Lookup("y"); !ok {
		t.Fatal("expected y to be visible from the enclosing scope found by walking up from the print statement")
	}

	found := false
	for sym := range scope.Visible() {
		if sym.Name == "y" {
			found = true
		}
	}
	if !found {
		t.Error("Visible() did not yield y")
	}
}

func TestFuncSignatureText_PlainFunc(t *testing.T) {
	tree, info := checkSrc(t, "func Insert(v int, n int) int {\n\treturn v + n\n}\n")
	decl := tree.Children(tree.Root)[0]

	if got, want := FuncSignatureText(tree, info, decl), "(v int, n int) int"; got != want {
		t.Errorf("FuncSignatureText = %q, want %q", got, want)
	}
}

func TestStructFieldsText_PlainStruct(t *testing.T) {
	tree, info := checkSrc(t, "struct Point {\n\tx int\n\ty int\n}\n"+
		"func f() int {\n\tp := Point{1, 2}\n\treturn p.x\n}\n")
	decl := tree.Children(tree.Root)[0]

	if got, want := StructFieldsText(tree, info, decl), "{ x int, y int }"; got != want {
		t.Errorf("StructFieldsText = %q, want %q", got, want)
	}
}

// TestFuncSignatureText_GenericMethod_ShowsInstantiatedTypes covers the
// Type-first half of FuncSignatureText's own contract: an instantiated
// generic method's own clone gets separately-checked Info.Types entries
// with the real substituted types, not the template's own literal "T".
func TestFuncSignatureText_GenericMethod_ShowsInstantiatedTypes(t *testing.T) {
	src := "struct Box[T] {\n\tvalue T\n}\n" +
		"func (Box[T]) Get() T {\n\treturn this.value\n}\n" +
		"func f() int {\n\tb := Box[int]{7}\n\treturn b.Get()\n}\n"
	tree, info := checkSrc(t, src)

	// The instantiated Get's own Symbol, reached via the call site's Refs
	// entry - not the template's, which never gets a Types entry at all.
	// "Get" lives in the MemberExpr's own Tok, not a child Ident node (see
	// ast.Node's own doc comment), so NodeAt on its offset is how to reach
	// it - not a name search.
	offset := strings.Index(src, "b.Get()") + len("b.")
	memberExpr := tree.NodeAt(lexer.Pos(offset))
	sym, ok := info.Refs[memberExpr]
	if !ok || sym == nil {
		t.Fatal("no symbol resolved for the b.Get() call site")
	}

	if got, want := FuncSignatureText(sym.Tree, info, sym.Decl), "() int"; got != want {
		t.Errorf("FuncSignatureText = %q, want %q (the instantiated Box[int]'s own substituted return type)", got, want)
	}
}

// TestFuncSignatureText_UnresolvedGenericTemplate_FallsBackToSourceText
// covers the raw-text fallback: a generic template is never checked (see
// ResolveTemplateForTooling's own doc comment - it populates Refs/Scopes
// only, never Types), so its own signature must still render something
// useful - exactly what was written, type parameter names included.
func TestFuncSignatureText_UnresolvedGenericTemplate_FallsBackToSourceText(t *testing.T) {
	tree, info := checkSrc(t, "func Sum[T](a T, b T) T {\n\treturn a + b\n}\n")
	decl := tree.Children(tree.Root)[0]

	if got, want := FuncSignatureText(tree, info, decl), "(a T, b T) T"; got != want {
		t.Errorf("FuncSignatureText = %q, want %q", got, want)
	}
}

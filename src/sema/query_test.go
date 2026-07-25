package sema

import "testing"

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

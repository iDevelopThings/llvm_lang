package ast

import (
	"testing"

	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// buildBinaryExpr builds `a + b` as a tiny BinaryExpr(Ident, Ident) tree,
// standing in for what the parser will eventually produce, so traversal can
// be tested before any grammar exists.
func buildBinaryExpr(t *testing.T) (*Tree, NodeIndex, NodeIndex, NodeIndex) {
	t.Helper()
	file := lexer.NewFile("t.ll", "a + b")
	lx := lexer.New(file)

	aTok := lx.Next()
	plusTok := lx.Next()
	bTok := lx.Next()

	tree := NewTree(file)
	left := tree.NewNode(enums.NodeKinds.Ident, aTok, Span{Start: aTok.Start, End: aTok.End})
	right := tree.NewNode(enums.NodeKinds.Ident, bTok, Span{Start: bTok.Start, End: bTok.End})
	bin := tree.NewNode(enums.NodeKinds.BinaryExpr, plusTok, Span{Start: aTok.Start, End: bTok.End}, left, right)
	return tree, bin, left, right
}

func TestChildAccess(t *testing.T) {
	tree, bin, left, right := buildBinaryExpr(t)

	if got := tree.Child(bin, 0); got != left {
		t.Errorf("Child(bin, 0) = %d, want %d (left)", got, left)
	}
	if got := tree.Child(bin, 1); got != right {
		t.Errorf("Child(bin, 1) = %d, want %d (right)", got, right)
	}
	if got := tree.Child(bin, 2); got != InvalidNode {
		t.Errorf("Child(bin, 2) = %d, want InvalidNode (out of range)", got)
	}

	kids := tree.Children(bin)
	if len(kids) != 2 || kids[0] != left || kids[1] != right {
		t.Errorf("Children(bin) = %v, want [%d %d]", kids, left, right)
	}
}

func TestParentLinkSetOnConstruction(t *testing.T) {
	tree, bin, left, right := buildBinaryExpr(t)

	if got := tree.Parent(left); got != bin {
		t.Errorf("Parent(left) = %d, want %d (bin)", got, bin)
	}
	if got := tree.Parent(right); got != bin {
		t.Errorf("Parent(right) = %d, want %d (bin)", got, bin)
	}
	if got := tree.Parent(bin); got != InvalidNode {
		t.Errorf("Parent(bin) = %d, want InvalidNode (bin is root)", got)
	}
}

func TestSiblingNavigation(t *testing.T) {
	tree, _, left, right := buildBinaryExpr(t)

	if got := tree.NextSibling(left); got != right {
		t.Errorf("NextSibling(left) = %d, want %d (right)", got, right)
	}
	if got := tree.PrevSibling(right); got != left {
		t.Errorf("PrevSibling(right) = %d, want %d (left)", got, left)
	}
	if got := tree.NextSibling(right); got != InvalidNode {
		t.Errorf("NextSibling(right) = %d, want InvalidNode (last child)", got)
	}
	if got := tree.PrevSibling(left); got != InvalidNode {
		t.Errorf("PrevSibling(left) = %d, want InvalidNode (first child)", got)
	}
}

func TestTextReadsMainToken(t *testing.T) {
	tree, bin, left, right := buildBinaryExpr(t)

	if got := tree.Text(left); got != "a" {
		t.Errorf("Text(left) = %q, want %q", got, "a")
	}
	if got := tree.Text(right); got != "b" {
		t.Errorf("Text(right) = %q, want %q", got, "b")
	}
	if got := tree.Text(bin); got != "+" {
		t.Errorf("Text(bin) = %q, want %q", got, "+")
	}
}

func TestInvalidNodeChildSlotSkipsParentStamp(t *testing.T) {
	// A fixed-arity node with an omitted optional child (InvalidNode in that
	// slot) must not panic trying to stamp a parent onto "nothing", and the
	// slot itself must still read back as InvalidNode.
	file := lexer.NewFile("t.ll", "a")
	lx := lexer.New(file)
	aTok := lx.Next()

	tree := NewTree(file)
	name := tree.NewNode(enums.NodeKinds.Ident, aTok, Span{Start: aTok.Start, End: aTok.End})
	decl := tree.NewNode(enums.NodeKinds.VarDecl, lexer.Token{}, Span{}, name, InvalidNode, InvalidNode)

	if got := tree.Child(decl, 1); got != InvalidNode {
		t.Errorf("Child(decl, 1) = %d, want InvalidNode (omitted type)", got)
	}
	if got := tree.Parent(name); got != decl {
		t.Errorf("Parent(name) = %d, want %d (decl)", got, decl)
	}
}

func TestDumpProducesIndentedOutline(t *testing.T) {
	tree, bin, _, _ := buildBinaryExpr(t)
	out := tree.Dump(bin)
	want := "BinaryExpr \"+\"\n  Ident \"a\"\n  Ident \"b\"\n"
	if out != want {
		t.Errorf("Dump() = %q, want %q", out, want)
	}
}

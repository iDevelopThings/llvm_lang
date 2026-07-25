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

// TestRootAncestor_DistinguishesRealTreeFromClone is the regression test
// for a real bug affecting References/DocumentHighlight: a monomorphized
// generic instantiation's own clone (CloneSubtree) is a real, separately
// addressable subtree whose own root is never wired as anyone's child -
// RootAncestor must tell the two apart, not just check for a nil-ish
// Parent (both a real tree's Root and a clone's own root have
// Parent == InvalidNode).
func TestRootAncestor_DistinguishesRealTreeFromClone(t *testing.T) {
	tree, bin, left, right := buildBinaryExpr(t)
	tree.Root = bin

	if got := tree.RootAncestor(left); got != tree.Root {
		t.Errorf("RootAncestor(left) = %d, want %d (tree.Root)", got, tree.Root)
	}
	if got := tree.RootAncestor(right); got != tree.Root {
		t.Errorf("RootAncestor(right) = %d, want %d (tree.Root)", got, tree.Root)
	}
	if got := tree.RootAncestor(bin); got != tree.Root {
		t.Errorf("RootAncestor(bin) = %d, want %d (bin is tree.Root itself)", got, tree.Root)
	}

	clone := tree.CloneSubtree(bin)
	if clone == tree.Root {
		t.Fatal("CloneSubtree produced the same NodeIndex as the original - test setup broken")
	}
	cloneChild := tree.Child(clone, 0)
	if got := tree.RootAncestor(cloneChild); got == tree.Root {
		t.Error("RootAncestor(a clone's own child) equals tree.Root - clone must be distinguishable")
	}
	if got := tree.RootAncestor(cloneChild); got != clone {
		t.Errorf("RootAncestor(a clone's own child) = %d, want %d (the clone's own unlinked root)", got, clone)
	}

	if got := tree.RootAncestor(InvalidNode); got != InvalidNode {
		t.Errorf("RootAncestor(InvalidNode) = %d, want InvalidNode", got)
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

// TestDescendants_PreOrderIncludesSelfAndEveryChild covers Descendants' own
// contract: n itself first, then each child's own subtree in declaration
// order - the shape a caller searching for a node kind anywhere under a
// subtree (not just at one known child slot) depends on.
func TestDescendants_PreOrderIncludesSelfAndEveryChild(t *testing.T) {
	tree, bin, left, right := buildBinaryExpr(t)

	var got []NodeIndex
	for n := range tree.Descendants(bin) {
		got = append(got, n)
	}
	want := []NodeIndex{bin, left, right}
	if len(got) != len(want) {
		t.Fatalf("Descendants(bin) = %v, want %v", got, want)
	}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("Descendants(bin)[%d] = %d, want %d", i, got[i], n)
		}
	}
}

// TestDescendants_LeafYieldsOnlyItself covers the base case: a node with no
// children yields exactly one entry, itself.
func TestDescendants_LeafYieldsOnlyItself(t *testing.T) {
	tree, _, left, _ := buildBinaryExpr(t)

	var got []NodeIndex
	for n := range tree.Descendants(left) {
		got = append(got, n)
	}
	if len(got) != 1 || got[0] != left {
		t.Errorf("Descendants(left) = %v, want [%d]", got, left)
	}
}

// TestDescendants_StopsEarlyWhenYieldReturnsFalse covers the iter.Seq
// contract every consumer of a range-over-func relies on: a break inside the
// range loop must stop the walk, not visit every remaining node regardless.
func TestDescendants_StopsEarlyWhenYieldReturnsFalse(t *testing.T) {
	tree, bin, _, _ := buildBinaryExpr(t)

	var got []NodeIndex
	for n := range tree.Descendants(bin) {
		got = append(got, n)
		break
	}
	if len(got) != 1 || got[0] != bin {
		t.Errorf("Descendants(bin) after one iteration = %v, want [%d]", got, bin)
	}
}

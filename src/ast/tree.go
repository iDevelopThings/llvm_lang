package ast

import (
	"fmt"
	"strings"

	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// Tree is the parsed syntax tree for one File: a flat, append-only Nodes
// array plus a shared child-index arena, built once by the parser and read
// many times afterward (analysis passes, codegen) - not a mutable,
// pointer-linked structure. See node.go for why Node itself carries indices
// rather than pointers.
type Tree struct {
	File *lexer.File

	// Root is the top-level node - the last one appended to Nodes, since a
	// node is only ever created once all its children already exist. Zero
	// (InvalidNode) until whoever's building the tree sets it.
	Root NodeIndex

	Nodes    []Node
	children []NodeIndex
}

// NewTree prepares an empty Tree over file, with slot 0 reserved as
// InvalidNode.
func NewTree(file *lexer.File) *Tree {
	t := &Tree{File: file}
	t.Nodes = append(t.Nodes, Node{})
	return t
}

// NewNode appends a node of kind and wires it to its already-built children:
// each child's Parent/IndexInParent is stamped immediately, since every
// child is fully constructed (and its index known) before its parent is -
// the natural order recursive-descent parsing builds a tree in. A child
// slot may be InvalidNode (a documented "this optional child was omitted"
// placeholder); IndexInParent is still assigned for it, but there's no node
// there to stamp a Parent onto.
func (t *Tree) NewNode(kind enums.NodeKind, tok lexer.Token, span Span, children ...NodeIndex) NodeIndex {
	start := int32(len(t.children))
	t.children = append(t.children, children...)

	idx := NodeIndex(len(t.Nodes))
	node := Node{
		Kind: kind,
		Tok:  tok,
		Span: span,
		Children: lexer.Range{
			Start: start,
			Count: int32(len(children)),
		},
	}
	t.Nodes = append(t.Nodes, node)

	for i, c := range children {
		if c == InvalidNode {
			continue
		}
		t.Nodes[c].Parent = idx
		t.Nodes[c].IndexInParent = int32(i)
	}
	return idx
}

// Parent returns n's parent, or InvalidNode for the root.
func (t *Tree) Parent(n NodeIndex) NodeIndex {
	return t.Nodes[n].Parent
}

// RootAncestor returns n's ultimate ancestor, walking Parent repeatedly
// until it finds a node with no parent of its own. For any node actually
// reachable from t.Root by walking Children, that's t.Root itself. For a
// monomorphized-generic instantiation's own clone (see CloneSubtree), whose
// root is never wired as anyone's child, it's the clone's own unlinked
// root instead - RootAncestor(n) != t.Root is the containment test for "is
// n really part of this file's own source tree, or a synthetic clone".
func (t *Tree) RootAncestor(n NodeIndex) NodeIndex {
	if n == InvalidNode {
		return InvalidNode
	}
	for {
		parent := t.Parent(n)
		if parent == InvalidNode {
			return n
		}
		n = parent
	}
}

// Children returns n's children in declaration order. The returned slice
// aliases the tree's shared arena; callers must not modify it.
func (t *Tree) Children(n NodeIndex) []NodeIndex {
	r := t.Nodes[n].Children
	return t.children[r.Start:r.End()]
}

// Child returns n's i-th child, or InvalidNode if i is out of range.
func (t *Tree) Child(n NodeIndex, i int) NodeIndex {
	kids := t.Children(n)
	if i < 0 || i >= len(kids) {
		return InvalidNode
	}
	return kids[i]
}

// NextSibling returns the node immediately after n in its parent's Children,
// or InvalidNode if n is the root or the last child.
func (t *Tree) NextSibling(n NodeIndex) NodeIndex {
	node := &t.Nodes[n]
	if node.Parent == InvalidNode {
		return InvalidNode
	}
	return t.Child(node.Parent, int(node.IndexInParent)+1)
}

// PrevSibling returns the node immediately before n in its parent's
// Children, or InvalidNode if n is the root or the first child.
func (t *Tree) PrevSibling(n NodeIndex) NodeIndex {
	node := &t.Nodes[n]
	if node.Parent == InvalidNode || node.IndexInParent == 0 {
		return InvalidNode
	}
	return t.Child(node.Parent, int(node.IndexInParent)-1)
}

// SpanOf returns n's full source span (see Node.Span).
func (t *Tree) SpanOf(n NodeIndex) Span {
	return t.Nodes[n].Span
}

// Text returns the exact source text of n's main token (see Node.Tok) - the
// identifier name, literal text, or operator symbol, depending on Kind.
// Nodes whose Kind doesn't use Tok return "".
func (t *Tree) Text(n NodeIndex) string {
	return t.File.Lexeme(t.Nodes[n].Tok)
}

// Dump renders the tree as an indented outline, for tests and debugging -
// not a stable/parseable format.
func (t *Tree) Dump(root NodeIndex) string {
	var b strings.Builder
	t.dump(&b, root, 0)
	return b.String()
}

func (t *Tree) dump(b *strings.Builder, n NodeIndex, depth int) {
	if n == InvalidNode {
		fmt.Fprintf(b, "%s<missing>\n", strings.Repeat("  ", depth))
		return
	}
	node := &t.Nodes[n]
	fmt.Fprintf(b, "%s%s", strings.Repeat("  ", depth), node.Kind)
	if node.Tok.Start != node.Tok.End {
		fmt.Fprintf(b, " %q", t.Text(n))
	}
	b.WriteByte('\n')
	for _, c := range t.Children(n) {
		t.dump(b, c, depth+1)
	}
}

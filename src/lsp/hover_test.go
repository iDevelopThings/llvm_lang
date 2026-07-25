package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// TestHover_FuncShowsSignatureDetail covers a real feature request: hovering
// a function must show its real parameter/return signature, not just its
// kind and bare name.
func TestHover_FuncShowsSignatureDetail(t *testing.T) {
	w, path := singleFileWorkspace(t, `func Insert(v int, n int) int {
	return v + n
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "Insert")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "(v int, n int) int") {
		t.Errorf("hover content = %q, want it to contain the function's own signature", content.Value)
	}
}

// TestHover_FuncHeaderAndSignatureAreOneLine is the regression test for a
// real user report: the kind+name heading ("**func** `Insert`") and its
// signature detail used to render as two disconnected markdown paragraphs -
// "func Insert" on one line, "(v int, n int) int" on the next - instead of
// one coherent "func Insert(v int, n int) int" declaration a client's Go
// grammar can highlight as a whole. Name and signature must now share a
// single fenced line.
func TestHover_FuncHeaderAndSignatureAreOneLine(t *testing.T) {
	w, path := singleFileWorkspace(t, `func Insert(v int, n int) int {
	return v + n
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "Insert")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "func Insert(v int, n int) int") {
		t.Errorf("hover content = %q, want \"func Insert(v int, n int) int\" on one line", content.Value)
	}
}

// TestHover_StructShowsFieldsDetail covers the same request for a struct:
// its actual field list, not just its doc comment.
func TestHover_StructShowsFieldsDetail(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Point {
	x int
	y int
}

func f() int {
	p := Point{1, 2}
	return p.x
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "{ x int, y int }") {
		t.Errorf("hover content = %q, want it to contain the struct's own field list", content.Value)
	}
}

// TestHover_StructHeaderAndFieldsAreOneLine is TestHover_FuncHeaderAndSignatureAreOneLine's
// struct-kind counterpart: "struct Point" and "{ x int, y int }" must share
// one fenced line ("struct Point { x int, y int }"), not render as two
// disconnected paragraphs.
func TestHover_StructHeaderAndFieldsAreOneLine(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Point {
	x int
	y int
}

func f() int {
	p := Point{1, 2}
	return p.x
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "struct Point { x int, y int }") {
		t.Errorf("hover content = %q, want \"struct Point { x int, y int }\" on one line", content.Value)
	}
}

// TestHover_ConstructorDoesNotRepeatItsOwnKindWord covers a gap the one-line
// header fix introduced: a constructor's own Symbol.Name already reads
// "<Struct>.constructor(<arity>)" (see resolve.go's declareConstructor), so
// naively prefixing "constructor " in front of it (the same treatment every
// other kind gets) would render the redundant "constructor
// Point.constructor(2)" - symbolDetail has no signature for this kind
// (SymConstructor isn't SymFunc), so hoverHeader must special-case it to
// show the name alone instead.
func TestHover_ConstructorDoesNotRepeatItsOwnKindWord(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Point {
	x int
	y int

	constructor(x int, y int) {
		this.x = x
		this.y = y
	}
}

func f() int {
	p := Point(1, 2)
	return p.x
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "constructor(x int, y int)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if strings.Contains(content.Value, "constructor Point.constructor") {
		t.Errorf("hover content = %q, want no redundant \"constructor\" prefix before Point.constructor(...)", content.Value)
	}
	if !strings.Contains(content.Value, "Point.constructor(2)") {
		t.Errorf("hover content = %q, want it to still show Point.constructor(2)", content.Value)
	}
}

// TestHover_StructShowsSizeAlignLayout covers the CLion-style memory-layout
// feature: hovering a struct must also show its real size/alignment/padding,
// not just its field list. flag (1 byte) forces a 3-byte gap before n (a
// 4-byte-aligned int) - the exact padding a naive "sum of field sizes" would
// miss.
func TestHover_StructShowsSizeAlignLayout(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Layout {
	flag bool
	n int
}

func f() int {
	l := Layout{true, 1}
	return l.n
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "struct Layout") + len("struct ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "size = 8, align = 4, padding = 3") {
		t.Errorf("hover content = %q, want it to contain the struct's real size/align/padding", content.Value)
	}
}

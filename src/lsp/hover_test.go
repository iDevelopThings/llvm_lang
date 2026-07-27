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

// TestHover_FieldShowsOwnSizeAlignOffsetWithPadding covers the CLion-style
// per-field hover: a field's own type+name on one line, which struct it
// belongs to, and its own Size (with any padding *after* it before the next
// field starts)/Alignment/Offset - not the whole struct's totals, which
// hovering the struct itself already shows.
func TestHover_FieldShowsOwnSizeAlignOffsetWithPadding(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Example {
	flag bool
	n int
}

func f() int {
	e := Example{true, 1}
	return e.n
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "flag bool")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "field flag bool") {
		t.Errorf("hover content = %q, want \"field flag bool\" on one line", content.Value)
	}
	if !strings.Contains(content.Value, "in struct `Example`") {
		t.Errorf("hover content = %q, want it to say which struct flag belongs to", content.Value)
	}
	if !strings.Contains(content.Value, "Size: 1 (+ 3 padding)") {
		t.Errorf("hover content = %q, want \"Size: 1 (+ 3 padding)\" - the 3 bytes before n", content.Value)
	}
	if !strings.Contains(content.Value, "Alignment: 1") {
		t.Errorf("hover content = %q, want \"Alignment: 1\"", content.Value)
	}
	if !strings.Contains(content.Value, "Offset: 0") {
		t.Errorf("hover content = %q, want \"Offset: 0\"", content.Value)
	}
}

// TestHover_LastFieldShowsNoPaddingNote covers the other direction: a field
// with nothing wasted after it (here, the struct's own last field, once
// rounded-up total size leaves no tail padding) must render a bare "Size: N"
// with no "(+ N padding)" note at all.
func TestHover_LastFieldShowsNoPaddingNote(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Pair {
	a int
	b int
}

func f() int {
	p := Pair{1, 2}
	return p.b
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "b int")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "Size: 4\n") {
		t.Errorf("hover content = %q, want a bare \"Size: 4\" with no padding note", content.Value)
	}
	if strings.Contains(content.Value, "padding") {
		t.Errorf("hover content = %q, want no padding mentioned for a field with none after it", content.Value)
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

// TestHover_OperatorShowsSignatureDetail covers the SymOperator gap
// symbolDetail used to have (unlike SymConstructor/SymDestructor, an
// operator overload does have a real declared param/return-type signature to
// show): hovering an `operator` declaration must render its own
// "(scalar f64) Vector2" signature, not fall through to the bare
// "operator Vector2.operator*(1)" the unhandled-kind default used to produce.
func TestHover_OperatorShowsSignatureDetail(t *testing.T) {
	w, path := singleFileWorkspace(t, vector2OperatorFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "operator *(scalar f64)") + len("operator ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "operator Vector2.operator*(1) (scalar f64) Vector2") {
		t.Errorf("hover content = %q, want it to contain the operator's own \"(scalar f64) Vector2\" signature", content.Value)
	}
}

// TestHover_ParamNameShowsType is the regression test for a real gap: hovering
// a parameter's own name (not its type annotation) resolved to an Ident with
// only an Info.Refs entry (see resolve.go's declareLocal) and no Info.Types
// entry of its own, so the type: line silently never rendered at all.
func TestHover_ParamNameShowsType(t *testing.T) {
	w, path := singleFileWorkspace(t, `func add(x int, y int) int {
	return x + y
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "x int")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "param x") {
		t.Errorf("hover content = %q, want it to still show \"param x\"", content.Value)
	}
	if !strings.Contains(content.Value, "type:\n```go\nint\n```") {
		t.Errorf("hover content = %q, want a \"type: int\" line", content.Value)
	}
}

// TestHover_ParamTypeShowsType is TestHover_ParamNameShowsType's counterpart:
// hovering a parameter's own type annotation must keep showing its type, now
// that the lookup redirects through the enclosing Param node (see
// ast.Tree.ParamOf) rather than reading the type child's own cached entry
// directly.
func TestHover_ParamTypeShowsType(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Point {
	x int
	y int
}

func f(p Point) int {
	return p.x
}
`)
	fa, _ := w.Analysis(path)
	typeOffset := strings.Index(fa.Tree.File.Src, "p Point") + len("p ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(typeOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "type:\n```go\nPoint\n```") {
		t.Errorf("hover content = %q, want a \"type: Point\" line", content.Value)
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

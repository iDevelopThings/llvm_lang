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

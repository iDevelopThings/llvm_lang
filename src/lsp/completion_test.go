package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func completionLabels(items []protocol.CompletionItem) []string {
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.Label
	}
	slices.Sort(labels)
	return labels
}

func TestCompletion_LocalsParamsAndGlobalsVisible(t *testing.T) {
	src := `var g int = 1

func other() int {
	return 0
}

func f(p int) int {
	x := 1
	print(x)
	return p
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "print(x)") + len("print(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	for _, want := range []string{"g", "other", "f", "p", "x"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels %v missing %q", labels, want)
		}
	}
}

func TestCompletion_StructMemberAtNestedDepth(t *testing.T) {
	src := `struct C {
	z int

	constructor(z int) {
		this.z = z
	}
}
struct B {
	c C
}
struct A {
	b B
}

func f(a A) int {
	a.b.c.
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "a.b.c.\n") + len("a.b.c.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"z"}; !slices.Equal(got, want) {
		t.Errorf("completion labels at a.b.c.<cursor> = %v, want %v (C's own field, resolved 3 levels deep)", got, want)
	}
}

func TestCompletion_StructMethodsIncluded(t *testing.T) {
	src := `struct Point {
	x int
}
func (Point) translate(dx int) {
	this.x = this.x + dx
}

func f(p Point) int {
	p.
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "p.\n") + len("p.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"translate", "x"}; !slices.Equal(got, want) {
		t.Errorf("completion labels at p.<cursor> = %v, want %v (field + method)", got, want)
	}
	for _, it := range items {
		if it.Label == "translate" && *it.Kind != protocol.CompletionItemKindMethod {
			t.Errorf("translate's Kind = %v, want CompletionItemKindMethod", *it.Kind)
		}
		if it.Label == "x" && *it.Kind != protocol.CompletionItemKindField {
			t.Errorf("x's Kind = %v, want CompletionItemKindField", *it.Kind)
		}
	}
}

func TestCompletion_EnumVariantsAndMethods(t *testing.T) {
	src := `enum Shape {
	Circle
	Square
}
func (Shape) describe() int {
	return 0
}

func f(s Shape) int {
	s.
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "s.\n") + len("s.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"describe"}; !slices.Equal(got, want) {
		t.Errorf("completion labels at s.<cursor> = %v, want %v (an enum value only ever exposes methods, never variants directly)", got, want)
	}
}

func TestCompletion_EnumTypeQualifiedVariantConstruction(t *testing.T) {
	src := `enum Shape {
	Circle
	Square
}
func (Shape) describe() int {
	return 0
}

func f() int {
	Shape.
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "Shape.\n") + len("Shape.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"Circle", "Square"}; !slices.Equal(got, want) {
		t.Errorf("completion labels at Shape.<cursor> = %v, want %v (unit-variant construction, never methods)", got, want)
	}
}

func TestCompletion_AlreadyImportedPackageMember(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
func helper() int {
	return 0
}
`, `
import "../mathutils"

func f() int {
	mathutils.
	return 0
}
`)
	result := analyzeProgram(prog, 1)
	var path string
	var fa *FileAnalysis
	for p, f := range result {
		if strings.Contains(p, "app") {
			path, fa = p, f
		}
	}
	if fa == nil {
		t.Fatal("app/main.llx not found in analysis result")
	}
	w := &Workspace{analysis: map[string]*FileAnalysis{path: fa}}

	offset := strings.Index(fa.Tree.File.Src, "mathutils.\n") + len("mathutils.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"Add"}; !slices.Equal(got, want) {
		t.Errorf("completion labels at mathutils.<cursor> = %v, want %v (only the exported func - helper is unexported)", got, want)
	}
}

func TestCompletion_DanglingMemberAccessAtEOF(t *testing.T) {
	src := `struct Foo {
	x int
}

func broken(f Foo) int {
	f.`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(len(fa.Tree.File.Src)))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"x"}; !slices.Equal(got, want) {
		t.Errorf("completion labels at f.<EOF> = %v, want %v", got, want)
	}
}

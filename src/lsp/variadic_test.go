// Variadic-parameter (`...T`) and spread-call (`x...`) coverage across every
// LSP capability - see src/lsp/doc.go's own standing convention (a new
// language feature needs this alongside coroutines_test.go/generics_test.go,
// not just functional sema/codegen tests) and AGENTS.md's note that generics
// landed with zero such coverage and a real bug went unnoticed for several
// rounds as a direct result.
package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// hoverText returns hover's own rendered markdown text, failing the test if
// hover is nil or not the expected MarkupContent shape - the small helper
// hover_test.go's own tests each inline; factored out here since every
// variadic hover test below needs it.
func hoverText(t *testing.T, hover *protocol.Hover) string {
	t.Helper()
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	return content.Value
}

const variadicFixture = `func Sum(nums ...int) int {
	total := 0
	for i := range nums {
		total = total + nums[i]
	}
	return total
}

struct Logger {
	prefix string
}

func (Logger) LogAll(tags ...string) int {
	return len(tags)
}

func main() int {
	collected := Sum(1, 2, 3)
	all := []int{4, 5, 6}
	spread := Sum(all...)
	l := Logger{"x"}
	return collected + spread + l.LogAll("a", "b")
}
`

// TestVariadic_Hover_FuncShowsEllipsisInSignature covers hovering the
// variadic function's own name: its rendered signature must show the `...`
// marker (paramListSignatureText), matching source, not silently drop it.
func TestVariadic_Hover_FuncShowsEllipsisInSignature(t *testing.T) {
	w, path := singleFileWorkspace(t, variadicFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "func Sum") + len("func ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "...int") {
		t.Errorf("Hover(Sum) = %q, want it to contain %q", text, "...int")
	}
}

// TestVariadic_Hover_ParamReferenceInsideBodyShowsSliceType covers hovering
// an ordinary reference to the variadic parameter inside the function's own
// body: its type must read as the real, effective `[]int` - an ordinary
// dynamic array, exactly like LANGUAGE.md's "Variadic parameters" section
// documents - not the bare declared element type `int`.
func TestVariadic_Hover_ParamReferenceInsideBodyShowsSliceType(t *testing.T) {
	w, path := singleFileWorkspace(t, variadicFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "range nums") + len("range ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "[]int") {
		t.Errorf("Hover(nums) = %q, want it to contain %q", text, "[]int")
	}
}

// TestVariadic_Definition_CollectCallSiteLandsOnDecl covers go-to-definition
// from an ordinary (collect-form) call site back to the variadic function's
// own declaration.
func TestVariadic_Definition_CollectCallSiteLandsOnDecl(t *testing.T) {
	w, path := singleFileWorkspace(t, variadicFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "Sum(1, 2, 3)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for the collect-form call site")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "func Sum") + len("func ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Sum's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestVariadic_Definition_SpreadCallSiteLandsOnDecl mirrors the collect-form
// test above for a spread call site (`Sum(all...)`) - the spread token must
// not confuse callee resolution.
func TestVariadic_Definition_SpreadCallSiteLandsOnDecl(t *testing.T) {
	w, path := singleFileWorkspace(t, variadicFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "Sum(all...)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for the spread call site")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "func Sum") + len("func ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Sum's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestVariadic_Completion_ParamVisibleInsideBody covers ordinary scope
// resolution for a variadic parameter: it must be visible in completion
// results inside the function's own body exactly like an ordinary parameter.
func TestVariadic_Completion_ParamVisibleInsideBody(t *testing.T) {
	w, path := singleFileWorkspace(t, variadicFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return total")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if !slices.Contains(labels, "nums") {
		t.Errorf("completion labels inside Sum's own body = %v, missing %q", labels, "nums")
	}
}

// TestVariadic_DocumentSymbolsAndFolding_MethodAndFreeFunc covers a method
// (Logger.LogAll) and a free function (Sum) both declaring a variadic last
// parameter - document symbols must list both, and their bodies must still
// fold like any other function.
func TestVariadic_DocumentSymbolsAndFolding_MethodAndFreeFunc(t *testing.T) {
	w, path := singleFileWorkspace(t, variadicFixture)

	syms := w.DocumentSymbols(path)
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range []string{"Sum", "Logger", "main"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q", names, want)
		}
	}

	folds := w.FoldingRanges(path)
	if len(folds) == 0 {
		t.Fatal("FoldingRanges returned none - a variadic function's own body must still fold like any other")
	}
}

// broken/incomplete-source variant every capability above needs to survive:
// a variadic parameter NOT in the last position (a real, rejected shape - see
// LANGUAGE.md's "Variadic parameters" section and the parser's own
// "only the last parameter may be variadic" diagnostic) plus a mid-typing
// `...` with no type after it yet, both in the same file so a single pass
// exercises both malformed shapes together.
func TestVariadic_MalformedVariadicParam_NoCrash(t *testing.T) {
	src := `func Bad(a ...int, b int) int {
	return b
}

func Typing(x ...
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a malformed variadic parameter list panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "x ...") + len("x ...")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	w.Hover(path, pos)
	w.Completion(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)

	badOffset := strings.Index(fa.Tree.File.Src, "func Bad") + len("func ")
	badPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(badOffset))
	w.Hover(path, badPos)
	w.Definition(path, badPos)
}

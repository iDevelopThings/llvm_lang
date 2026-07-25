// Generator/async-coroutine coverage across every LSP capability. An audit
// found these features already worked correctly everywhere (no new
// block-shaped AST nodes, scopes resolve normally inside an async/
// generator body, semantic tokens' keyword-coverage pass already rescues
// `async`/`await`/`yield` the same way it does `else`/`import`/`map`) -
// but src/lsp had zero tests actually exercising either feature before
// this file, which is how the generics regression this whole round started
// from went unnoticed for as long as it did. See src/lsp/doc.go for the
// standing convention this file exists to satisfy.
package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

const coroutinesFixture = `struct Resource {
	id int

	constructor(v int) {
		this.id = v
	}
	destructor() {
		print(this.id)
	}
}

async func Sequence() {
	a := Resource(1)
	print(100)
	await
	b := Resource(2)
	print(200)
}

func Range(a int, b int) yield int {
	for i := a; i < b; i++ {
		yield i
	}
}

func driveToCompletion() {
	h := Sequence()
	for !done(h) {
		resume(h)
	}
	delete h
}

func main() int {
	driveToCompletion()
	total := 0
	for v := range Range(0, 5) {
		total = total + v
	}
	return total
}
`

// TestCoroutines_SemanticTokens_KeywordsAndBuiltins covers the exact class
// of bug this session already found and fixed once before for else/import/
// map: a keyword consumed by the parser but never stored as any node's own
// Tok falls through collectNodeTokens entirely, relying on
// collectLexicalExtras' own re-lex pass to catch it. async/await/yield are
// new keywords this feature introduced - resume/done are ordinary
// predeclared functions, not keywords, and must classify as such.
func TestCoroutines_SemanticTokens_KeywordsAndBuiltins(t *testing.T) {
	w, path := singleFileWorkspace(t, coroutinesFixture)
	fa, _ := w.Analysis(path)

	toks := w.SemanticTokens(path)
	if toks == nil {
		t.Fatal("SemanticTokens returned nil")
	}

	lines := strings.Split(fa.Tree.File.Src, "\n")
	kindAt := func(line, char, length int) string {
		return lines[line][char : char+length]
	}

	line, char := 0, 0
	keywordCount := map[string]int{}
	functionCount := map[string]int{}
	for i := 0; i+4 < len(toks.Data); i += 5 {
		deltaLine, deltaChar, length, typeIdx := int(toks.Data[i]), int(toks.Data[i+1]), int(toks.Data[i+2]), int(toks.Data[i+3])
		if deltaLine == 0 {
			char += deltaChar
		} else {
			line += deltaLine
			char = deltaChar
		}
		text := kindAt(line, char, length)
		switch typeIdx {
		case semTokKeyword:
			keywordCount[text]++
		case semTokFunction:
			functionCount[text]++
		}
	}

	for _, want := range []string{"async", "await", "yield"} {
		if keywordCount[want] == 0 {
			t.Errorf("keyword %q: got 0 tokens, want at least 1", want)
		}
	}
	for _, want := range []string{"resume", "done"} {
		if functionCount[want] == 0 {
			t.Errorf("%q: got 0 Function tokens, want at least 1 (a predeclared func, not a keyword)", want)
		}
	}
}

func TestCoroutines_Hover_AsyncFuncAndGenerator(t *testing.T) {
	w, path := singleFileWorkspace(t, coroutinesFixture)
	fa, _ := w.Analysis(path)

	for _, name := range []string{"Sequence", "Range"} {
		offset := strings.Index(fa.Tree.File.Src, name)
		pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))
		if hover := w.Hover(path, pos); hover == nil {
			t.Errorf("Hover(%s) returned nil", name)
		}
	}
}

func TestCoroutines_DocumentSymbols_ShowsAsyncAndGeneratorFuncs(t *testing.T) {
	w, path := singleFileWorkspace(t, coroutinesFixture)

	syms := w.DocumentSymbols(path)
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range []string{"Sequence", "Range", "driveToCompletion", "main"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q", names, want)
		}
	}
}

func TestCoroutines_FoldingRanges_BodiesFold(t *testing.T) {
	w, path := singleFileWorkspace(t, coroutinesFixture)

	folds := w.FoldingRanges(path)
	if len(folds) == 0 {
		t.Fatal("FoldingRanges returned none - an async/generator func body must still fold like any other")
	}
}

// TestCoroutines_Completion_IdentifierInsideAsyncBodySeesLocals covers
// ordinary scope resolution inside an async body - confirmed by the audit
// to already work (resolveFuncBody treats an async FuncDecl identically to
// any other), locked in here as a real regression test rather than just a
// code trace.
func TestCoroutines_Completion_IdentifierInsideAsyncBodySeesLocals(t *testing.T) {
	w, path := singleFileWorkspace(t, coroutinesFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "print(200)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	for _, want := range []string{"a", "b"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels inside Sequence's own body = %v, missing %q", labels, want)
		}
	}
}

// TestCoroutines_DanglingMemberAccessInsideAsyncBody_NoCrash is the
// broken/incomplete-source variant every capability above needs to
// survive, per this project's own invalid-path-coverage standard.
func TestCoroutines_DanglingMemberAccessInsideAsyncBody_NoCrash(t *testing.T) {
	src := `struct Resource {
	id int
}
async func Sequence() {
	a := Resource(1)
	a.
}
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a dangling member access inside an async func body panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "a.\n") + len("a.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	w.Hover(path, pos)
	w.Completion(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)
}

package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"
)

// TestSemanticTokens_UncapturedKeywords covers the three keywords no
// ast.Node's own Tok ever captures - "else", "import" (via a real
// cross-package program - see loadProgram/analyze_test.go), and "map" in
// map[K]V type position - asserting each gets exactly one keyword token
// (collectLexicalExtras' own re-lex pass), and that an already-node-captured
// keyword ("if"/"func"/"var") is never emitted a second time (the coverage
// check collectNodeTokens/collectLexicalExtras share via a lexer.Pos map).
func TestSemanticTokens_UncapturedKeywords(t *testing.T) {
	src := `var m map[string]int

func main() int {
	found := true
	if found {
		return 1
	} else {
		return 0
	}
}
`
	file := lexer.NewFile("test.llx", src)
	tree, diags := parser.ParseFile(file, false)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}

	covered := make(map[lexer.Pos]bool)
	var raw []rawToken
	collectNodeTokens(tree, nil, tree.Root, make(map[*sema.Symbol]bool), covered, &raw)
	collectLexicalExtras(file.Name, file.Src, covered, &raw)

	lines := strings.Split(src, "\n")
	keywordCount := make(map[string]int)
	for _, tok := range raw {
		if tok.typeIdx != semTokKeyword {
			continue
		}
		keywordCount[lines[tok.line][tok.char:tok.char+tok.length]]++
	}

	for _, want := range []string{"var", "map", "func", "if", "else"} {
		if got := keywordCount[want]; got != 1 {
			t.Errorf("keyword %q: got %d tokens, want exactly 1", want, got)
		}
	}
}

// TestSemanticTokens_NilGetsKeywordClassification is the regression case
// for a real reported bug: nil isn't a lexer keyword like true/false (see
// scope.go's own universe-scope doc comment - it's a predeclared identifier
// resolved via scope, SymBuiltinValue), so it fell all the way through
// classifyIdentToken's default case and rendered as a plain variable
// instead of reading like the literal it is.
func TestSemanticTokens_NilGetsKeywordClassification(t *testing.T) {
	src := `struct Point {
	x int
}

func f() int {
	var p *Point = nil
	if p == nil {
		return 0
	}
	return 1
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(src, "= nil") + len("= ")
	tok, ok := semanticTokenAt(fa, lexer.Pos(offset))
	if !ok {
		t.Fatal("no semantic token emitted for nil")
	}
	if tok.typeIdx != semTokKeyword {
		t.Errorf("token type = %d, want semTokKeyword (%d) - nil fell back to the plain-variable classification",
			tok.typeIdx, semTokKeyword)
	}
}

// TestSemanticTokens_UnresolvedIdentifierGetsReadonlyFallback is the
// regression case for a real reported bug: an unresolved identifier (here,
// simulated the same way TestSemanticTokens_UncapturedKeywords already does
// via a nil Info - matching what a generic template's own unresolved body
// looks like today) must still carry modReadonly on its fallback variable
// classification. Without it, LSP4IJ's default color mapping renders every
// such identifier as REASSIGNED_LOCAL_VARIABLE (underlined), regardless of
// whether it's actually a variable at all - confirmed against a real
// screenshot of a whole generic struct rendering as a wall of underlines.
func TestSemanticTokens_UnresolvedIdentifierGetsReadonlyFallback(t *testing.T) {
	src := `func f() int {
	return x
}
`
	file := lexer.NewFile("test.llx", src)
	tree, diags := parser.ParseFile(file, false)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}

	covered := make(map[lexer.Pos]bool)
	var raw []rawToken
	collectNodeTokens(tree, nil, tree.Root, make(map[*sema.Symbol]bool), covered, &raw)

	found := false
	for _, tok := range raw {
		if tok.typeIdx != semTokVariable {
			continue
		}
		found = true
		if tok.modifiers&modReadonly == 0 {
			t.Errorf("unresolved variable token modifiers = %d, want modReadonly set", tok.modifiers)
		}
	}
	if !found {
		t.Fatal("expected at least one semTokVariable token for the unresolved identifier")
	}
}

// TestSemanticTokens_ImportKeyword covers "import" specifically - it needs
// a real second package to resolve against (see loadProgram, defined in
// analyze_test.go), since ImportDecl's own Tok is the string path, not the
// "import" keyword before it.
func TestSemanticTokens_ImportKeyword(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

func main() int {
	return mathutils.Add(2, 3)
}
`)

	result := analyzeProgram(prog, 1)
	var appFA *FileAnalysis
	for path, fa := range result {
		if strings.Contains(path, "app") {
			appFA = fa
		}
	}
	if appFA == nil {
		t.Fatal("app/main.llx not found in analysis result")
	}

	covered := make(map[lexer.Pos]bool)
	var raw []rawToken
	collectNodeTokens(appFA.Tree, appFA.Info, appFA.Tree.Root, make(map[*sema.Symbol]bool), covered, &raw)
	collectLexicalExtras(appFA.Tree.File.Name, appFA.Tree.File.Src, covered, &raw)

	lines := strings.Split(appFA.Tree.File.Src, "\n")
	found := 0
	for _, tok := range raw {
		if tok.typeIdx == semTokKeyword && lines[tok.line][tok.char:tok.char+tok.length] == "import" {
			found++
		}
	}
	if found != 1 {
		t.Errorf(`"import" keyword: got %d tokens, want exactly 1`, found)
	}
}

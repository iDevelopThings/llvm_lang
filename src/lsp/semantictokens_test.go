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
	tree, diags := parser.ParseFile(file)
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

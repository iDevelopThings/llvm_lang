// stub func coverage across major LSP capabilities (see src/lsp/doc.go).
// Loader skips stubs.llx as a package member, so the happy-path workspace
// below builds FileAnalysis directly for a stubs.llx basename - the same
// resolve/check path an editor would want when that file is open. The
// broken-path tests use ordinary singleFileWorkspace (main.llx).
package lsp

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

const stubsFixture = `stub func args() []string
stub func print(x Any)
stub func AnyAs[T](a Any) (T, bool)
`

func stubsFileWorkspace(t *testing.T, src string) (w *Workspace, path string) {
	t.Helper()
	path = filepath.Join(string(filepath.Separator), "std", "stubs.llx")
	tree, pdiags := parser.ParseFile(lexer.NewFile(path, src), false)
	if pdiags.HasErrors() {
		t.Fatalf("parse errors: %v", pdiags.All())
	}
	info, rdiags := sema.Resolve(tree)
	cdiags := sema.Check(tree, info)
	bag := diag.NewBag()
	for d := range pdiags.Seq() {
		emitDiag(bag, d)
	}
	for d := range rdiags.Seq() {
		emitDiag(bag, d)
	}
	for d := range cdiags.Seq() {
		emitDiag(bag, d)
	}
	sema.ResolveTemplatesForTooling(tree, info)
	fa := &FileAnalysis{
		Tree:       tree,
		Info:       info,
		Diags:      bag,
		Generation: 1,
	}
	return &Workspace{analysis: map[string]*FileAnalysis{path: fa}}, path
}

func emitDiag(bag *diag.Bag, d diag.Diagnostic) {
	if d.Severity == diag.SeverityWarning {
		bag.WarnfLabel(d.Pos, d.End, d.Label, "%s", d.Msg)
		return
	}
	bag.ErrorfLabel(d.Pos, d.End, d.Label, "%s", d.Msg)
}

func TestStubFunc_SemanticTokens_StubKeyword(t *testing.T) {
	w, path := stubsFileWorkspace(t, stubsFixture)
	fa, _ := w.Analysis(path)

	toks := w.SemanticTokens(path)
	if toks == nil {
		t.Fatal("SemanticTokens returned nil")
	}
	lines := strings.Split(fa.Tree.File.Src, "\n")
	keywordCount := map[string]int{}
	line, char := 0, 0
	for i := 0; i+4 < len(toks.Data); i += 5 {
		line += int(toks.Data[i])
		if toks.Data[i] != 0 {
			char = 0
		}
		char += int(toks.Data[i+1])
		length := int(toks.Data[i+2])
		typeIdx := int(toks.Data[i+3])
		if typeIdx != semTokKeyword {
			continue
		}
		text := lines[line][char : char+length]
		keywordCount[text]++
	}
	if keywordCount["stub"] == 0 {
		t.Errorf("keyword %q: got 0 tokens, want at least 1", "stub")
	}
	if keywordCount["func"] == 0 {
		t.Errorf("keyword %q: got 0 tokens, want at least 1", "func")
	}
}

func TestStubFunc_DocumentSymbols_ShowsStubFuncs(t *testing.T) {
	w, path := stubsFileWorkspace(t, stubsFixture)
	syms := w.DocumentSymbols(path)
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range []string{"args", "print", "AnyAs"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q", names, want)
		}
	}
}

func TestStubFunc_Hover_ShowsSignature(t *testing.T) {
	w, path := stubsFileWorkspace(t, stubsFixture)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "args")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "args") {
		t.Errorf("hover content = %q, want it to mention args", content.Value)
	}
}

func TestStubFunc_Diagnostics_RejectedOutsideStubsFile(t *testing.T) {
	w, path := singleFileWorkspace(t, "stub func args() []string\n")
	fa, _ := w.Analysis(path)
	found := false
	for _, d := range fa.Diags.All() {
		if strings.Contains(d.Msg, "stubs.llx") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("diagnostics = %v, want mention of stubs.llx", fa.Diags.All())
	}
}

func TestStubFunc_SemanticTokens_StubKeywordInBrokenFile(t *testing.T) {
	// Broken placement still lexes `stub` - the keyword must classify even
	// when check rejects the decl (same class of bug as async/await/tests).
	w, path := singleFileWorkspace(t, "stub func args() []string\n")
	fa, _ := w.Analysis(path)
	toks := w.SemanticTokens(path)
	if toks == nil {
		t.Fatal("SemanticTokens returned nil")
	}
	lines := strings.Split(fa.Tree.File.Src, "\n")
	line, char := 0, 0
	found := false
	for i := 0; i+4 < len(toks.Data); i += 5 {
		line += int(toks.Data[i])
		if toks.Data[i] != 0 {
			char = 0
		}
		char += int(toks.Data[i+1])
		length := int(toks.Data[i+2])
		typeIdx := int(toks.Data[i+3])
		if typeIdx == semTokKeyword && lines[line][char:char+length] == "stub" {
			found = true
			break
		}
	}
	if !found {
		t.Error(`no Keyword token found for "stub" in illegal main.llx placement`)
	}
}

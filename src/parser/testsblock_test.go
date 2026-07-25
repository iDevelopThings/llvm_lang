package parser

import (
	"strings"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// This file covers `tests { ... }` (see LANGUAGE.md's "tests{}" section):
// the parse-time splice/wrap decision (see DECISIONS.md) that lets test code
// live in the same file as the code it tests without leaking into a normal
// build. decl_test.go's TestDeclShape-style Tree.Dump conventions apply here
// too.

// TestTestsBlockWrappedOutsideTestMode covers the default (non -test) parse:
// a tests{} block's contents are wrapped in one TestBlockDecl node - see
// ast.Node's own TestBlockDecl doc comment for why that's enough to keep it
// invisible to every downstream pass.
func TestTestsBlockWrappedOutsideTestMode(t *testing.T) {
	src := "func main() int {\n\treturn 0\n}\n\ntests {\n\tfunc TestAdd(t *test.Runner) {\n\t}\n}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.All())
	}
	decls := tree.Children(tree.Root)
	if len(decls) != 2 {
		t.Fatalf("File has %d top-level decls, want 2:\n%s", len(decls), tree.Dump(tree.Root))
	}
	if got := tree.Nodes[decls[1]].Kind; got != enums.NodeKinds.TestBlockDecl {
		t.Fatalf("decls[1] kind = %s, want TestBlockDecl:\n%s", got, tree.Dump(tree.Root))
	}
	inner := tree.Children(decls[1])
	if len(inner) != 1 || tree.Nodes[inner[0]].Kind != enums.NodeKinds.FuncDecl {
		t.Fatalf("TestBlockDecl children = %v, want a single FuncDecl:\n%s", inner, tree.Dump(tree.Root))
	}
}

// TestTestsBlockSplicedInTestMode covers the same source under testMode=true:
// the tests{} block's contents are spliced as ordinary top-level siblings,
// with no TestBlockDecl node anywhere in the tree.
func TestTestsBlockSplicedInTestMode(t *testing.T) {
	src := "func main() int {\n\treturn 0\n}\n\ntests {\n\tfunc TestAdd(t *test.Runner) {\n\t}\n}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src), true)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.All())
	}
	decls := tree.Children(tree.Root)
	if len(decls) != 2 {
		t.Fatalf("File has %d top-level decls, want 2 (spliced):\n%s", len(decls), tree.Dump(tree.Root))
	}
	for _, d := range decls {
		if tree.Nodes[d].Kind == enums.NodeKinds.TestBlockDecl {
			t.Fatalf("found a TestBlockDecl node in test mode:\n%s", tree.Dump(tree.Root))
		}
	}
	if got := tree.Nodes[decls[1]].Kind; got != enums.NodeKinds.FuncDecl {
		t.Fatalf("decls[1] kind = %s, want FuncDecl (spliced):\n%s", got, tree.Dump(tree.Root))
	}
	if got := tree.Text(tree.FuncName(decls[1])); got != "TestAdd" {
		t.Errorf("spliced func name = %q, want TestAdd", got)
	}
}

// TestTestsBlockWithImport covers a tests{} block containing its own
// import - the actual leak this feature exists to prevent: wrapped, it's
// buried inside an invisible TestBlockDecl (loader's own import scan only
// ever walks tree.Children(tree.Root) at ImportDecl kind, so it never sees
// this one); spliced, it's an ordinary top-level ImportDecl like any other.
func TestTestsBlockWithImport(t *testing.T) {
	src := "tests {\n\timport \"std:test\"\n\n\tfunc TestFoo(t *test.Runner) {\n\t}\n}\n"

	t.Run("wrapped", func(t *testing.T) {
		tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
		if diags.HasErrors() {
			t.Fatalf("unexpected parse errors: %v", diags.All())
		}
		decls := tree.Children(tree.Root)
		if len(decls) != 1 || tree.Nodes[decls[0]].Kind != enums.NodeKinds.TestBlockDecl {
			t.Fatalf("expected a single TestBlockDecl, got:\n%s", tree.Dump(tree.Root))
		}
		for n := range tree.TopLevelDeclsOfKind(enums.NodeKinds.ImportDecl) {
			t.Fatalf("found a top-level ImportDecl outside test mode: %v", n)
		}
		inner := tree.Children(decls[0])
		if len(inner) != 2 || tree.Nodes[inner[0]].Kind != enums.NodeKinds.ImportDecl {
			t.Fatalf("expected [ImportDecl, FuncDecl] inside TestBlockDecl, got:\n%s", tree.Dump(tree.Root))
		}
	})

	t.Run("spliced", func(t *testing.T) {
		tree, diags := ParseFile(lexer.NewFile("t.ll", src), true)
		if diags.HasErrors() {
			t.Fatalf("unexpected parse errors: %v", diags.All())
		}
		decls := tree.Children(tree.Root)
		if len(decls) != 2 || tree.Nodes[decls[0]].Kind != enums.NodeKinds.ImportDecl {
			t.Fatalf("expected [ImportDecl, FuncDecl] spliced at top level, got:\n%s", tree.Dump(tree.Root))
		}
	})
}

// TestNestedTestsBlockRejected covers the one illegal shape: a tests{}
// directly inside another must be reported, in both modes - but still
// parsed/recovered rather than bailing the whole file.
func TestNestedTestsBlockRejected(t *testing.T) {
	src := "tests {\n\ttests {\n\t\tfunc TestInner(t *test.Runner) {\n\t\t}\n\t}\n}\n"
	for _, testMode := range []bool{false, true} {
		tree, diags := ParseFile(lexer.NewFile("t.ll", src), testMode)
		if diags.ErrorCount() != 1 {
			t.Fatalf("testMode=%v: ErrorCount = %d, want 1: %v", testMode, diags.ErrorCount(), diags.All())
		}
		if !strings.Contains(diags.All()[0].Msg, "nested") {
			t.Errorf("testMode=%v: diagnostic = %q, want it to mention nesting", testMode, diags.All()[0].Msg)
		}
		if tree.Root == ast.InvalidNode {
			t.Fatalf("testMode=%v: expected a usable tree, got none", testMode)
		}
	}
}

// TestTestsBlockRejectedInsideFunctionBody covers the grammar-position
// restriction: `tests{}` is legal only where a top-level declaration is,
// never inside a function body - it isn't dispatched by parseStmt at all, so
// it falls through to expression position and hits parseIdentExpr's own
// unhandled-keyword default, same as `struct`/`enum`/`import` already do
// there.
func TestTestsBlockRejectedInsideFunctionBody(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "func f() {\n\ttests {\n\t}\n}\n"))
	p.parseTopLevelItem()
	if !p.diags.HasErrors() {
		t.Fatalf("expected an error for tests{} inside a function body")
	}
}

// TestTestsBlockMissingClosingBraceRecovers covers the malformed_test.go-style
// bounded-diagnostic convention: an unclosed tests{} must not hang or crash,
// and must not derail the rest of the file's parse.
func TestTestsBlockMissingClosingBraceRecovers(t *testing.T) {
	src := "tests {\n\tfunc TestFoo(t *test.Runner) {\n\t}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
	if !diags.HasErrors() {
		t.Fatalf("expected a diagnostic for the unclosed tests{} block")
	}
	if diags.ErrorCount() >= maxErrors {
		t.Fatalf("ErrorCount = %d hit the bailout threshold on trivial input", diags.ErrorCount())
	}
	if tree.Root == ast.InvalidNode {
		t.Fatalf("expected a usable tree even after the unclosed block")
	}
}

// TestTestsBlockGarbageInsideRecovers covers garbage inside a tests{} body -
// it must degrade to a bounded diagnostic via the same recovery
// parseTopLevelItem's default case already provides, and still let a
// following real declaration in the outer file parse normally.
func TestTestsBlockGarbageInsideRecovers(t *testing.T) {
	src := "tests {\n\t)\n}\n\nfunc f() { }\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
	if !diags.HasErrors() {
		t.Fatalf("expected a diagnostic for the stray ')' inside tests{}")
	}
	if diags.ErrorCount() >= maxErrors {
		t.Fatalf("ErrorCount = %d hit the bailout threshold on trivial input", diags.ErrorCount())
	}
	decls := tree.Children(tree.Root)
	if len(decls) != 2 {
		t.Fatalf("File has %d top-level decls, want 2 (TestBlockDecl + FuncDecl): %s", len(decls), tree.Dump(tree.Root))
	}
	if got := tree.Nodes[decls[1]].Kind; got != enums.NodeKinds.FuncDecl {
		t.Errorf("decls[1] kind = %s, want FuncDecl (recovery must still reach it)", got)
	}
}

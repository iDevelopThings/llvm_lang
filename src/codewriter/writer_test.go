package codewriter_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"llvm_lang/src/codewriter"
)

func TestLineAndIndent(t *testing.T) {
	w := codewriter.New()
	w.Line("outer")
	w.Indent(func() {
		w.Line("inner")
		w.Indent(func() {
			w.Line("deeper")
		})
	})
	w.Line("outer-again")

	want := "" +
		"outer\n" +
		"\tinner\n" +
		"\t\tdeeper\n" +
		"outer-again\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestBraceAndParen(t *testing.T) {
	w := codewriter.New()
	w.Paren("import", func() {
		w.Line(`"fmt"`)
		w.Line(`"iter"`)
	})
	w.Blank()
	w.Bracef("type %s struct", "Point", func() {
		w.Line("X int")
		w.Line("Y int")
	})

	want := "" +
		"import (\n" +
		"\t\"fmt\"\n" +
		"\t\"iter\"\n" +
		")\n" +
		"\n" +
		"type Point struct {\n" +
		"\tX int\n" +
		"\tY int\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNestedBlocks(t *testing.T) {
	w := codewriter.New()
	w.Bracef("func %s()", "Values", func() {
		w.Brace("if len(xs) == 0", func() {
			w.Line("return nil")
		})
		w.Line("return xs")
	})

	want := "" +
		"func Values() {\n" +
		"\tif len(xs) == 0 {\n" +
		"\t\treturn nil\n" +
		"\t}\n" +
		"\treturn xs\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestErrorAwareGroups(t *testing.T) {
	wantErr := errors.New("body failed")
	w := codewriter.New()
	err := w.BraceErr("outer", func() error {
		w.Line("before")
		return w.ParenErr("inner", func() error {
			w.Line("partial")
			return wantErr
		})
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	w.Line("after")
	want := "" +
		"outer {\n" +
		"\tbefore\n" +
		"\tinner (\n" +
		"\t\tpartial\n" +
		"after\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestErrorAwareGroupsSuccess(t *testing.T) {
	w := codewriter.New()
	err := w.BraceErr("outer", func() error {
		return w.ParenErr("inner", func() error {
			w.Line("value")
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "" +
		"outer {\n" +
		"\tinner (\n" +
		"\t\tvalue\n" +
		"\t)\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentRestoredAfterPanic(t *testing.T) {
	w := codewriter.New()
	func() {
		defer func() {
			_ = recover()
		}()
		w.Indent(func() {
			panic("stop")
		})
	}()
	w.Line("after")

	if got := w.String(); got != "after\n" {
		t.Fatalf("got %q, want unindented output", got)
	}
}

func TestOpenClose(t *testing.T) {
	w := codewriter.New()
	w.Open("func Foo()", "{")
	w.Line("return 1")
	w.Close("}")

	want := "" +
		"func Foo() {\n" +
		"\treturn 1\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestComments(t *testing.T) {
	w := codewriter.New()
	w.Comment("Code generated. DO NOT EDIT.")
	w.Comments(
		"Point is a 2D point.",
		"Zero value is usable.",
	)
	w.Brace("type Point struct", func() {
		w.Comment("X is the horizontal axis.")
		w.Line("X int")
	})

	want := "" +
		"// Code generated. DO NOT EDIT.\n" +
		"// Point is a 2D point.\n" +
		"// Zero value is usable.\n" +
		"type Point struct {\n" +
		"\t// X is the horizontal axis.\n" +
		"\tX int\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSpacesIndentAndHashComments(t *testing.T) {
	w := codewriter.New(
		codewriter.IndentUnit("    "),
		codewriter.CommentPrefix("#"),
	)
	w.Comment("python-ish")
	w.Line("def foo():")
	w.Indent(func() {
		w.Line("return 1")
	})

	want := "" +
		"# python-ish\n" +
		"def foo():\n" +
		"    return 1\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintMidLine(t *testing.T) {
	w := codewriter.New()
	w.Print("return foo(")
	w.NL()
	w.Indent(func() {
		w.Line("a,")
		w.Line("b,")
	})
	w.Line(")")

	want := "" +
		"return foo(\n" +
		"\ta,\n" +
		"\tb,\n" +
		")\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRaw(t *testing.T) {
	w := codewriter.New()
	w.Indent(func() {
		w.Raw("already-indented\n")
		w.Line("auto")
	})

	want := "" +
		"already-indented\n" +
		"\tauto\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestEmptyHeaderGroup(t *testing.T) {
	w := codewriter.New()
	w.Brace("", func() {
		w.Line("1,")
		w.Line("2,")
	})

	want := "" +
		"{\n" +
		"\t1,\n" +
		"\t2,\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestGroupf(t *testing.T) {
	w := codewriter.New()
	w.Groupf("func (%s) %s", "(", ")", "c *Container", "Values", func() {
		w.Line("return nil")
	})

	want := "" +
		"func (c *Container) Values (\n" +
		"\treturn nil\n" +
		")\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLinefNoArgsSkipsFmt(t *testing.T) {
	w := codewriter.New()
	// No args → raw write. fmt.Sprintf("%%") would collapse to "%".
	w.Linef("%%")
	if got := w.String(); got != "%%\n" {
		t.Fatalf("got %q", got)
	}
}

func TestZeroValueWriter(t *testing.T) {
	var w codewriter.Writer
	w.Line("package main")
	w.Brace("func main()", func() {
		w.Line(`println("hi")`)
	})

	want := "" +
		"package main\n" +
		"func main() {\n" +
		"\tprintln(\"hi\")\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReset(t *testing.T) {
	w := codewriter.New()
	w.Line("first")
	w.Reset()
	w.Line("second")
	if got := w.String(); got != "second\n" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteTo(t *testing.T) {
	w := codewriter.New()
	w.Line("hello")
	var buf bytes.Buffer
	n, err := w.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 || buf.String() != "hello\n" {
		t.Fatalf("n=%d buf=%q", n, buf.String())
	}
}

func TestBracefPanicsWithoutBody(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	w := codewriter.New()
	w.Bracef("type %s struct", "Foo")
}

func TestBracefPanicsOnNonFuncBody(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	w := codewriter.New()
	w.Bracef("type %s struct", "Foo", "not-a-func")
}

func TestBlankAfterContent(t *testing.T) {
	w := codewriter.New()
	w.Line("a")
	w.Blank()
	w.Line("b")
	if got := w.String(); got != "a\n\nb\n" {
		t.Fatalf("got %q", got)
	}
}

func TestCommentf(t *testing.T) {
	w := codewriter.New()
	w.Commentf("from %s", "spec.yml")
	if got := w.String(); got != "// from spec.yml\n" {
		t.Fatalf("got %q", got)
	}
}

func TestGrow(t *testing.T) {
	w := codewriter.New(codewriter.Grow(256))
	w.Line(strings.Repeat("x", 100))
	if w.Len() != 101 {
		t.Fatalf("len=%d", w.Len())
	}
}

// Structural resemblance smoke test — mirrors the enum_codegen shape without
// the manual \t counting.
func TestStructuralShape(t *testing.T) {
	constPrefix := "Lexemes"
	T := "Lexeme"
	members := []struct{ Name, Wire string }{
		{"Ident", "1"},
		{"Number", "2"},
	}

	w := codewriter.New()
	w.Comment("Code generated. DO NOT EDIT.")
	w.Line("package enums")
	w.Blank()
	w.Paren("import", func() {
		w.Line(`"fmt"`)
	})
	w.Blank()
	w.Linef("type %s uint16", T)
	w.Blank()
	w.Paren("const", func() {
		for _, m := range members {
			w.Linef("%s%s %s = %s", constPrefix, m.Name, T, m.Wire)
		}
	})
	w.Blank()
	w.Bracef("type %sContainer struct", T, func() {
		for _, m := range members {
			w.Linef("%s %s", m.Name, T)
		}
	})

	want := "" +
		"// Code generated. DO NOT EDIT.\n" +
		"package enums\n" +
		"\n" +
		"import (\n" +
		"\t\"fmt\"\n" +
		")\n" +
		"\n" +
		"type Lexeme uint16\n" +
		"\n" +
		"const (\n" +
		"\tLexemesIdent Lexeme = 1\n" +
		"\tLexemesNumber Lexeme = 2\n" +
		")\n" +
		"\n" +
		"type LexemeContainer struct {\n" +
		"\tIdent Lexeme\n" +
		"\tNumber Lexeme\n" +
		"}\n"
	if got := w.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

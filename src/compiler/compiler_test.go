package compiler

import (
	"path/filepath"
	"strings"
	"testing"

	"llvm_lang/src/loader"

	"github.com/spf13/afero"
)

// TestCompilePackage_Success covers a valid multi-file, import-less package
// (see LANGUAGE.md's "Multi-file packages" section): a struct+method
// declared in one file, used from another, plus a free function in a third -
// Result.Module must come back non-nil with no diagnostics anywhere, and
// must actually be usable (a real, verified LLVM module).
func TestCompilePackage_Success(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "main.llx", Src: `
func main() int {
	p := Point{1, 2}
	p.move(10, 20)
	return double(p.x) + p.y
}
`},
		{Name: "point.llx", Src: `
struct Point {
	x int
	y int
}

func (Point) move(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}
`},
		{Name: "math.llx", Src: `
func double(x int) int {
	return x * 2
}
`},
	})
	t.Cleanup(func() {
		if res.Module != nil {
			res.Module.Dispose()
		}
	})

	if res.Module == nil {
		t.Fatalf("Module = nil, want a real module; VerifyErr = %v, diags = %v", res.VerifyErr, dumpDiags(res))
	}
	if len(res.Trees) != 3 {
		t.Errorf("len(Trees) = %d, want 3", len(res.Trees))
	}
	for _, tree := range res.Trees {
		if b := res.Diags[tree]; b == nil || b.Len() != 0 {
			t.Errorf("tree %q has diagnostics, want none: %v", tree.File.Name, dumpDiags(res))
		}
	}
}

// TestCompileProgram_Success covers a valid two-package program linked by an
// `import` declaration (see LANGUAGE.md's "Imports" section), built over an
// afero.MemMapFs so no real filesystem is touched.
func TestCompileProgram_Success(t *testing.T) {
	fs := afero.NewMemMapFs()
	sep := string(filepath.Separator)
	writeFile := func(path, src string) {
		if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(sep, "prog", "mathutils", "add.llx"), `
func Add(a int, b int) int {
	return a + b
}
`)
	writeFile(filepath.Join(sep, "prog", "app", "main.llx"), `
import "../mathutils"

func main() int {
	return mathutils.Add(2, 3)
}
`)

	prog, err := loader.LoadProgram(fs, filepath.Join(sep, "prog", "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}

	res := CompileProgram(prog)
	t.Cleanup(func() {
		if res.Module != nil {
			res.Module.Dispose()
		}
	})

	if res.Module == nil {
		t.Fatalf("Module = nil, want a real module; VerifyErr = %v, diags = %v", res.VerifyErr, dumpDiags(res))
	}
	if len(res.Trees) != 2 {
		t.Errorf("len(Trees) = %d, want 2", len(res.Trees))
	}
}

// TestCompilePackage_ParseError covers a real syntax error: Module must be
// nil, and the offending tree's own bag must record it - the pipeline must
// never reach sema/codegen at all.
func TestCompilePackage_ParseError(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "t.llx", Src: `
func main() {
	print(
}
`},
	})

	if res.Module != nil {
		t.Fatalf("Module = non-nil, want nil on a parse error")
		res.Module.Dispose()
	}
	if !anyDiagContains(res, "expect") && !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic somewhere, got: %v", dumpDiags(res))
	}
}

// TestCompilePackage_ResolveError covers a real sema.ResolvePackage failure -
// a reference to an undeclared name - which must stop the pipeline before
// sema.CheckProgram/codegen ever runs.
func TestCompilePackage_ResolveError(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "t.llx", Src: `
func main() {
	print(doesNotExist)
}
`},
	})

	if res.Module != nil {
		res.Module.Dispose()
		t.Fatal("Module = non-nil, want nil on a resolve error")
	}
	if !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic, got: %v", dumpDiags(res))
	}
}

// TestCompilePackage_CheckError covers a real sema.CheckProgram (type-check)
// failure - assigning a string literal to an int-typed var - which must stop
// the pipeline before codegen ever runs.
func TestCompilePackage_CheckError(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "t.llx", Src: `
func main() {
	var a int = "oops"
	print(a)
}
`},
	})

	if res.Module != nil {
		res.Module.Dispose()
		t.Fatal("Module = non-nil, want nil on a type-check error")
	}
	if !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic, got: %v", dumpDiags(res))
	}
}

// TestCompilePackage_CodegenError covers a real codegen-level restriction
// sema itself has no opinion on (see AGENTS.md's "Global var initializers
// must be compile-time constants" section): a top-level var whose
// initializer calls a function. sema.Check passes this fine; only
// codegen.GeneratePackage rejects it.
func TestCompilePackage_CodegenError(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "t.llx", Src: `
func get() int {
	return 5
}

var a int = get()

func main() {
	print(a)
}
`},
	})

	if res.Module != nil {
		res.Module.Dispose()
		t.Fatal("Module = non-nil, want nil on a codegen error")
	}
	if !anyDiagContains(res, "compile-time constant") {
		t.Errorf("expected a diagnostic mentioning \"compile-time constant\", got: %v", dumpDiags(res))
	}
}

func anyDiagHasErrors(res *Result) bool {
	for _, b := range res.Diags {
		if b.HasErrors() {
			return true
		}
	}
	return false
}

func anyDiagContains(res *Result, substr string) bool {
	for _, b := range res.Diags {
		for _, d := range b.All() {
			if strings.Contains(d.Msg, substr) {
				return true
			}
		}
	}
	return false
}

func dumpDiags(res *Result) []string {
	var out []string
	for _, tree := range res.Trees {
		b := res.Diags[tree]
		if b == nil {
			continue
		}
		for _, d := range b.All() {
			out = append(out, tree.File.Name+": "+d.Msg)
		}
	}
	return out
}

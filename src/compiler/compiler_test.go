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
	p.translate(10, 20)
	return double(p.x) + p.y
}
`},
		{Name: "point.llx", Src: `
struct Point {
	x int
	y int
}

func (Point) translate(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}
`},
		{Name: "math.llx", Src: `
func double(x int) int {
	return x * 2
}
`},
	}, true)
	t.Cleanup(func() {
		if res.Module != nil {
			res.Module.Dispose()
			res.TargetMachine.Dispose()
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

	res := CompileProgram(prog, true)
	t.Cleanup(func() {
		if res.Module != nil {
			res.Module.Dispose()
			res.TargetMachine.Dispose()
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
	}, true)

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
	}, true)

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
	}, true)

	if res.Module != nil {
		res.Module.Dispose()
		t.Fatal("Module = non-nil, want nil on a type-check error")
	}
	if !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic, got: %v", dumpDiags(res))
	}
}

// TestCompilePackage_NonConstantGlobalInitializer covers the feature
// documented in CODEGEN.md's "Global var initializers" section end to end,
// through this package's own real pipeline (not just codegen in isolation -
// see src/codegen/globals_test.go for the JIT-executed coverage of this same
// feature): a top-level var whose initializer calls a function used to be a
// codegen-level error; it's real, legal code now, lowered into a synthesized
// init function rather than folded at compile time.
func TestCompilePackage_NonConstantGlobalInitializer(t *testing.T) {
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
	}, true)
	t.Cleanup(func() {
		if res.Module != nil {
			res.Module.Dispose()
			res.TargetMachine.Dispose()
		}
	})

	if res.Module == nil {
		t.Fatalf("Module = nil, want a real module; VerifyErr = %v, diags = %v", res.VerifyErr, dumpDiags(res))
	}
}

// TestCompilePackage_CodegenError covers the one class of codegen-only
// diagnostic that survives the non-constant-global-initializer round (see
// src/codegen/constfold.go's isConstFoldable doc comment): a genuine error
// inside an initializer that's still structurally constant-*shaped* (both
// operands of `/` are literals) - division by zero - rather than deferred to
// a synthesized init function the way an actually non-constant initializer
// (TestCompilePackage_NonConstantGlobalInitializer above) now is.
func TestCompilePackage_CodegenError(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "t.llx", Src: `
var a int = 5 / 0

func main() {
	print(a)
}
`},
	}, true)

	if res.Module != nil {
		res.Module.Dispose()
		t.Fatal("Module = non-nil, want nil on a codegen error")
	}
	if !anyDiagContains(res, "division by zero") {
		t.Errorf("expected a diagnostic mentioning \"division by zero\", got: %v", dumpDiags(res))
	}
}

// TestCompilePackage_ShadowedMakeCheckError covers a real, once-panicking
// bug end-to-end: shadowing the predeclared `make` with an ordinary
// same-named function (legal shadowing, see sema/scope.go's universeScope)
// and calling it with make's own bespoke argument grammar still in play
// (the parser's isMakeCallee dispatches purely on the callee's lexical
// spelling, forcing the first "argument" into a type-position ArrayType
// node regardless of what `make` actually resolves to - see
// parser/expr.go). Before the fix, this "type-checked" with zero
// diagnostics and reached codegen.GeneratePackage's genExpr, which has no
// case for a bare ArrayType and panics. sema.CheckProgram must now report a
// real diagnostic and stop the pipeline right there, exactly like
// TestCompilePackage_CheckError above - codegen must never run at all.
func TestCompilePackage_ShadowedMakeCheckError(t *testing.T) {
	res := CompilePackage([]loader.SourceFile{
		{Name: "t.llx", Src: `
func make(a int, b int) int {
	return a + b
}

func main() int {
	return make([]int, 2)
}
`},
	}, true)

	if res.Module != nil {
		res.Module.Dispose()
		t.Fatal("Module = non-nil, want nil on a shadowed-make type-check error")
	}
	if !anyDiagContains(res, "array type used as a value") {
		t.Errorf("expected a diagnostic mentioning \"array type used as a value\", got: %v", dumpDiags(res))
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

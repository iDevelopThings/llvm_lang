package codegen

import (
	"testing"

	"llvm_lang/src/codegen/stdoutcapture"
)

// captureStdout is a thin, test-package-local wrapper around
// stdoutcapture.Capture (see that package's doc comment for why the actual
// cgo/freopen logic can't live directly in this _test.go file - the Go
// toolchain flatly rejects `import "C"` in any test file).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	data, err := stdoutcapture.Capture(fn)
	if err != nil {
		t.Fatalf("captureStdout: %v", err)
	}
	return string(data)
}

// TestPrintI64FormatSpecifierIsCorrect is the empirical test AGENTS.md's
// codegen section documents: this project's toolchain is mingw64/MSYS2 on
// Windows (see SETUP.md), making a raw extern `printf` call - and %lld's
// behavior for a 64-bit value can differ between MSVCRT-style and
// mingw-ANSI-stdio-style printf implementations on Windows (MSVCRT's own
// historic printf doesn't understand %lld at all, wanting %I64d instead;
// mingw-w64's x86_64 default enables __USE_MINGW_ANSI_STDIO, which redirects
// printf to its own C99-compliant implementation supporting %lld correctly).
// Rather than assume either behavior, this JIT-executes a real print of a
// value that doesn't fit in 32 bits (4000000000 - printing it correctly
// requires actually treating the argument as 64-bit; a garbled/truncated
// interpretation would produce visibly different digits) and asserts the
// captured output is byte-for-byte correct - see stdoutcapture.Capture.
func TestPrintI64FormatSpecifierIsCorrect(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	var a i64 = 4000000000
	print(a)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "4000000000\n"
	if out != want {
		t.Fatalf("print(i64(4000000000)) captured stdout = %q, want %q", out, want)
	}
}

// TestPrintNegativeI64FormatSpecifierIsCorrect covers the same specifier for
// a negative i64 value, whose sign only makes sense if printf actually reads
// all 64 bits as signed (a 32-bit-only misinterpretation would print an
// entirely different, wrong number, not just a truncated one).
func TestPrintNegativeI64FormatSpecifierIsCorrect(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	var a i64 = -9000000000
	print(a)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "-9000000000\n"
	if out != want {
		t.Fatalf("print(i64(-9000000000)) captured stdout = %q, want %q", out, want)
	}
}

// TestPrintEveryNumericWidthAndFloat is a real, output-asserted (not just
// "didn't crash") test of print's format-string dispatch for every new
// numeric type - i8/i16/i32/i64/f32/f64 - now that captureStdout makes a
// real assertion possible instead of the "runs to completion" smoke test
// TestPrintDoesNotCrash (string_test.go) was limited to before this file
// existed.
func TestPrintEveryNumericWidthAndFloat(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	var a i8 = -12
	var b i16 = -1234
	var c i32 = 123456
	var d i64 = 4000000000
	var e f32 = 2.5
	var f f64 = 3.5
	print(a)
	print(b)
	print(c)
	print(d)
	print(e)
	print(f)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "-12\n-1234\n123456\n4000000000\n2.500000\n3.500000\n"
	if out != want {
		t.Fatalf("print output = %q, want %q", out, want)
	}
}

// TestPrintStructWithEveryFieldKindRendersBareValuesCorrectly covers
// genPrintValueBare/genPrintStringValueBare - the "no trailing newline"
// counterparts genPrintStructValue/genPrintArrayValue use per field/element
// - for every field kind besides plain i32/struct/array (already covered by
// TestPrintStructAndArray, string_test.go): string, bool, i8, i16, i64, f32,
// and f64 struct fields. Those bare branches only ever execute when a
// struct/array value with one of these field kinds is actually printed - a
// top-level `print(x)` on the same kinds instead goes through genPrintCall's
// own (already well-covered) non-bare branches, so nesting one inside a
// struct is the only way to reach them at all.
func TestPrintStructWithEveryFieldKindRendersBareValuesCorrectly(t *testing.T) {
	jm := compileAndJIT(t, `
struct Mixed {
	s string
	b bool
	a i8
	c i16
	d i64
	e f32
	f f64
}

func main() {
	m := Mixed{"hi", true, -12, -1234, 4000000000, 2.5, 3.5}
	print(m)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "{hi true -12 -1234 4000000000 2.500000 3.500000}\n"
	if out != want {
		t.Fatalf("print(m) captured stdout = %q, want %q", out, want)
	}
}

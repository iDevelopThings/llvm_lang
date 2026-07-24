package codegen

import "testing"

// TestPrintUnsignedWidths asserts print's unsigned format-string dispatch
// (fmtUInt/fmtUInt64) renders each width correctly - crucially a u64 above
// 2^63 and a u32 above 2^31, which the signed "%d"/"%lld" path would render as
// a negative number. See CODEGEN.md's printf-specifier note.
func TestPrintUnsignedWidths(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	var a u8 = 200
	var b u16 = 60000
	var c u32 = 4000000000
	var d u64 = 18446744073709551615
	print(a)
	print(b)
	print(c)
	print(d)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "200\n60000\n4000000000\n18446744073709551615\n"
	if out != want {
		t.Fatalf("print output = %q, want %q", out, want)
	}
}

// TestPrintStructWithUnsignedFields covers genPrintValueBare's unsigned cases
// (the no-newline counterparts) via a struct holding each unsigned width.
func TestPrintStructWithUnsignedFields(t *testing.T) {
	jm := compileAndJIT(t, `
struct U {
	a u8
	b u16
	c u32
	d u64
}

func main() {
	u := U{200, 60000, 4000000000, 18446744073709551615}
	print(u)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "{200 60000 4000000000 18446744073709551615}\n"
	if out != want {
		t.Fatalf("print(u) = %q, want %q", out, want)
	}
}

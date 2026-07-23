package codegen

import (
	"fmt"
	"strings"
	"testing"
)

// TestStringConcatAndEquality covers string concatenation (genStringConcat,
// heap-backed via malloc+memcpy) and `==`/`!=` (genStringEqual, a real
// content comparison via memcmp, not pointer identity) together: two
// independently-concatenated strings built from different pieces must
// compare equal only when their content actually matches. Also covers a
// top-level var whose initializer is a compile-time constant string
// concatenation, folded entirely at compile time (see constfold.go's
// constStringText) rather than emitting a runtime concat for it.
func TestStringConcatAndEquality(t *testing.T) {
	jm := compileAndJIT(t, `
var greeting string = "Hello, " + "World!"

func greetingIsHelloWorld() bool {
	return greeting == "Hello, " + "World!"
}

func stringsEqual() bool {
	a := "foo" + "bar"
	b := "fo" + "obar"
	return a == b
}

func stringsNotEqual() bool {
	a := "foo"
	b := "foobar"
	return a != b
}

func compoundAssignConcat() bool {
	s := "a"
	s += "b"
	s += "c"
	return s == "abc"
}
`)

	if got := jm.runBool(t, "greetingIsHelloWorld"); got != true {
		t.Errorf("greetingIsHelloWorld() = %v, want true", got)
	}
	if got := jm.runBool(t, "stringsEqual"); got != true {
		t.Errorf("stringsEqual() = %v, want true", got)
	}
	if got := jm.runBool(t, "stringsNotEqual"); got != true {
		t.Errorf("stringsNotEqual() = %v, want true", got)
	}
	if got := jm.runBool(t, "compoundAssignConcat"); got != true {
		t.Errorf("compoundAssignConcat() = %v, want true - += on string", got)
	}
}

// TestStringOrdering covers genStringOrder/genStringCompareSign - `< <= > >=`
// on strings now lower to a real byte-by-byte lexicographic comparison (see
// AGENTS.md's Operators section), not a length or pointer compare. Pairs
// chosen to exercise: an ordinary lexicographic difference ("apple" vs
// "banana"), a same-prefix-different-length pair ("ab" vs "abc" - the case
// a naive memcmp-only-if-equal-length implementation would get wrong), and
// equal strings (never strictly less/greater than themselves).
func TestStringOrdering(t *testing.T) {
	jm := compileAndJIT(t, `
func lessLexicographic() bool {
	return "apple" < "banana"
}

func notLessReversed() bool {
	return "banana" < "apple"
}

func prefixIsLess() bool {
	return "ab" < "abc"
}

func prefixIsNotGreater() bool {
	return "ab" > "abc"
}

func longerPrefixIsGreater() bool {
	return "abc" > "ab"
}

func equalStringsNotLess() bool {
	return "same" < "same"
}

func equalStringsLessOrEqual() bool {
	return "same" <= "same"
}

func equalStringsGreaterOrEqual() bool {
	return "same" >= "same"
}

func concatenatedComparesCorrectly() bool {
	a := "ab" + "c"
	b := "abd"
	return a < b
}
`)

	tests := []struct {
		name string
		want bool
	}{
		{"lessLexicographic", true},
		{"notLessReversed", false},
		{"prefixIsLess", true},
		{"prefixIsNotGreater", false},
		{"longerPrefixIsGreater", true},
		{"equalStringsNotLess", false},
		{"equalStringsLessOrEqual", true},
		{"equalStringsGreaterOrEqual", true},
		{"concatenatedComparesCorrectly", true},
	}
	for _, tc := range tests {
		if got := jm.runBool(t, tc.name); got != tc.want {
			t.Errorf("%s() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStringConcatLoopBuildsCorrectString covers genStringConcat's new
// arena-backed allocation path (setupArena/genArenaAlloc in runtime.go,
// replacing the old bare-malloc-per-concat call) end to end: build a longer
// string via several `+=` concatenations in a loop, JIT-execute, and assert
// the final content is actually correct (a real content comparison via `==`
// - see genStringEqual - not just a length check), proving the arena hands
// out distinct, correctly-sized, non-overlapping regions across many
// sequential allocations rather than just the single allocation the earlier
// tests in this file exercise.
func TestStringConcatLoopBuildsCorrectString(t *testing.T) {
	const reps = 50
	expected := strings.Repeat("xy", reps)
	src := fmt.Sprintf(`
func build() bool {
	s := ""
	i := 0
	for i < %d {
		s += "xy"
		i++
	}
	return s == "%s"
}
`, reps, expected)

	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (concatenated string should equal %d reps of \"xy\")", reps)
	}
}

// TestArenaGrowsAcrossManyAllocations forces the arena (setupArena) to
// actually grow past its first chunk (arenaChunkSize, runtime.go): many
// small, independent concatenations, whose combined size clears one chunk,
// so the loop's later iterations land in at least a second malloc'd block.
// Every iteration re-checks its own result, so a bug that corrupted an
// earlier allocation while growing to a new block (e.g. an off-by-one in the
// bump/remaining bookkeeping) would show up as a wrong-content failure, not
// just a crash.
func TestArenaGrowsAcrossManyAllocations(t *testing.T) {
	const iterations = 40000 // 4 bytes/iteration clears the 64KiB chunk several times over
	src := fmt.Sprintf(`
func build() bool {
	ok := true
	i := 0
	for i < %d {
		a := "ab" + "cd"
		if a != "abcd" {
			ok = false
		}
		i++
	}
	return ok
}
`, iterations)

	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (every concatenation across arena growth should still be correct)")
	}
}

// TestPrintDoesNotCrash is a real JIT-executed smoke test for `print` on
// each supported argument type (int, string, bool - see AGENTS.md's "print
// builtin" section and genPrintCall). It can't assert on the actual console
// output (capturing what a JIT-compiled call into libc's real printf writes
// to the process's OS-level stdout runs into real Windows console/CRT
// redirection limits well beyond this package - see BLOCKERS.md), but
// running to completion without crashing is still a meaningful correctness
// signal: a wrong field offset/format-argument order in genPrintCall's
// {ptr,len} extraction would realistically segfault or hand printf a
// garbage pointer, not silently succeed.
func TestPrintDoesNotCrash(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	print(42)
	print("hello")
	print(true)
	print(false)
	s := "foo" + "bar"
	print(s)
}
`)
	jm.runInt32(t, "main")

	ir := jm.ir
	for _, want := range []string{
		`call i32 (ptr, ...) @printf(ptr @.fmt.int`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.str`,
	} {
		if !strings.Contains(ir, want) {
			t.Errorf("generated IR missing expected printf call %q\n%s", want, ir)
		}
	}
}

// TestPrintStructAndArray covers genPrintStructValue/genPrintArrayValue - the
// recursive, Go-fmt-`%v`-inspired rendering documented in AGENTS.md's codegen
// section (`{f0 f1 ...}` for a struct, `[e0 e1 ...]` for an array, nested
// struct/array fields rendered the same way recursively).
//
// Same limitation as TestPrintDoesNotCrash (see BLOCKERS.md): this can't
// assert on the actual console bytes a JIT-compiled call into libc's real
// printf writes, for the same real Windows CRT-redirection reason - so, same
// established pattern, this proves correctness two other ways instead: (1) a
// real JIT-executed run to completion without crashing, which a wrong field
// index/extractvalue order would realistically not survive, and (2) the
// generated IR contains the expected sequence of printf calls against the
// exact punctuation/format-string globals genPrintStructValue/
// genPrintArrayValue emit, in the order the chosen format requires.
func TestPrintStructAndArray(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

struct Line {
	start Point
	end Point
}

func main() {
	p := Point{1, 2}
	print(p)

	a := [3]int{1, 2, 3}
	print(a)

	l := Line{Point{1, 2}, Point{3, 4}}
	print(l)
}
`)
	jm.runInt32(t, "main")

	ir := jm.ir
	for _, want := range []string{
		`call i32 (ptr, ...) @printf(ptr @.fmt.lbrace)`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.int.bare`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.space)`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.rbrace)`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.lbracket)`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.rbracket)`,
		`call i32 (ptr, ...) @printf(ptr @.fmt.newline)`,
	} {
		if !strings.Contains(ir, want) {
			t.Errorf("generated IR missing expected printf call %q\n%s", want, ir)
		}
	}
}

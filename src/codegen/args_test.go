package codegen

import (
	"strings"
	"testing"
)

// TestArgsCallUnderJITReturnsEmptySlice covers the documented JIT-execution
// fallback for args() (see args.go's own doc comment and LANGUAGE.md's "The
// args() builtin" section): llvm_lang.args_init is never invoked under JIT
// (unlike a real AOT-compiled binary, where it runs automatically via
// @llvm.global_ctors before main - see cmd/llvmc's TestBinary_AOT_Args), so
// llvm_lang.args stays at its zero-initialized value - an empty []string,
// len 0.
func TestArgsCallUnderJITReturnsEmptySlice(t *testing.T) {
	jm := compileAndJIT(t, `
func main() int {
	return len(args())
}
`)
	if got := jm.runInt32(t, "main"); got != 0 {
		t.Errorf("len(args()) under JIT = %d, want 0 (documented JIT fallback)", got)
	}
}

// TestArgsCallableFromNonMainFunction covers LANGUAGE.md's own claim that
// args() is callable from anywhere, not just main - a plain helper function
// works exactly the same.
func TestArgsCallableFromNonMainFunction(t *testing.T) {
	jm := compileAndJIT(t, `
func helper() int {
	return len(args())
}

func main() int {
	return helper()
}
`)
	if got := jm.runInt32(t, "main"); got != 0 {
		t.Errorf("helper()'s len(args()) under JIT = %d, want 0", got)
	}
}

// TestArgsUnusedProgramHasNoArgsMachinery covers a real, deliberate codegen
// design point (see args.go's own doc comment for buildArgsInitFn): a
// program that never calls args() anywhere gets none of the extra
// external-symbol surface that feature would otherwise need (__argc/__argv,
// llvm_lang.args_init) - only the always-present, fully self-contained
// llvm_lang.args global itself (see setupArgsGlobal). This is what keeps
// every *other* existing program's JIT-execution behavior completely
// unaffected by this feature's existence.
func TestArgsUnusedProgramHasNoArgsMachinery(t *testing.T) {
	mod := compileSrc(t, `
func main() int {
	return 0
}
`)
	defer mod.Dispose()
	ir := mod.LLVM.String()

	for _, sym := range []string{"__argc", "__argv", "llvm_lang.args_init"} {
		if strings.Contains(ir, sym) {
			t.Errorf("IR unexpectedly contains %q for a program that never calls args():\n%s", sym, ir)
		}
	}
	if !strings.Contains(ir, "llvm_lang.args") {
		t.Errorf("expected the always-present llvm_lang.args global regardless, IR:\n%s", ir)
	}
}

// TestArgsUsedProgramHasArgsMachinery is TestArgsUnusedProgramHasNoArgsMachinery's
// positive counterpart: a program that does call args() somewhere gets
// __argc/__argv declared, llvm_lang.args_init built, and registered into
// @llvm.global_ctors - the machinery a real AOT-compiled binary needs to
// actually marshal real argv at startup (see cmd/llvmc's TestBinary_AOT_Args
// for that end-to-end proof).
func TestArgsUsedProgramHasArgsMachinery(t *testing.T) {
	mod := compileSrc(t, `
func main() int {
	return len(args())
}
`)
	defer mod.Dispose()
	ir := mod.LLVM.String()

	for _, sym := range []string{"__argc", "__argv", "llvm_lang.args_init", "@llvm.global_ctors"} {
		if !strings.Contains(ir, sym) {
			t.Errorf("expected IR to contain %q for a program calling args(), IR:\n%s", sym, ir)
		}
	}
}

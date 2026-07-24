package codegen

import (
	"strings"
	"testing"
)

// --- `cfunc(T1, T2) R` - bare C function pointer types (see LANGUAGE.md's
// "External functions (FFI)" section): lowers to a bare g.ptrTy, called
// directly with no ctxPtr at all - genCFuncCall's own calling convention,
// distinct from an ordinary func value's fat-pointer genIndirectCall. ---

// TestCFuncParamLowersToBarePointerIR covers llvmType's TypeCFunc case: an
// extern func's own cfunc-typed parameter declares as a plain `ptr`, never
// the `{ ptr, ptr }` fat pointer an ordinary func-typed parameter would
// need (see llvmType's TypeFunc case) - there is no ctxPtr slot to make
// room for at all.
func TestCFuncParamLowersToBarePointerIR(t *testing.T) {
	jm := compileAndJIT(t, `
extern func apply_callback(cb cfunc(int) int, x int) int

func main() int {
	return 0
}
`)
	if !strings.Contains(jm.ir, "declare i32 @apply_callback(ptr, i32)") {
		t.Errorf("expected `declare i32 @apply_callback(ptr, i32)`, got:\n%s", jm.ir)
	}
}

// TestCFuncCallHasNoCtxPtrExtractionIR covers genCFuncCall's own calling
// convention directly against the generated IR: calling through a
// cfunc-typed parameter is a plain `call` straight through the raw pointer
// parameter, with no `extractvalue` at all - unlike genIndirectCall's own
// ordinary func-typed call, which must extract both fnPtr and ctxPtr out of
// a real fat-pointer value first (see CODEGEN.md's "First-class functions"
// section).
func TestCFuncCallHasNoCtxPtrExtractionIR(t *testing.T) {
	jm := compileAndJIT(t, `
func callThrough(cb cfunc(int) int, x int) int {
	return cb(x)
}

func main() int {
	return 0
}
`)
	if strings.Contains(jm.ir, "extractvalue") {
		t.Errorf("expected no extractvalue (no fat pointer/ctxPtr) in a cfunc call, got:\n%s", jm.ir)
	}
	// The call's callee operand is a loaded register (`%N`), never a fat
	// pointer's own extracted fnPtr - genCFuncCall calls straight through
	// cb's own loaded value with no ctxPtr threaded in as a leading
	// argument, unlike genIndirectCallValue's `call i32 %fnPtr(ptr %ctxPtr,
	// i32 ...)` shape for an ordinary func-typed value.
	if !strings.Contains(jm.ir, "= call i32 %") {
		t.Errorf("expected a direct `call i32 %%N(...)` through the bare cfunc pointer, got:\n%s", jm.ir)
	}
}

// TestTopLevelFuncToCFuncPassesRealAddressNotThunkIR covers
// checkFuncToCFuncConversion/genExpr's own Ident case: converting a
// top-level func to cfunc passes that function's real address (`@double`)
// directly - never `.thunk` (genFuncThunk's own memoized uniform-ABI
// adapter, which an ordinary func-to-func-value conversion always goes
// through instead - see genFuncValue).
func TestTopLevelFuncToCFuncPassesRealAddressNotThunkIR(t *testing.T) {
	jm := compileAndJIT(t, `
func callThrough(cb cfunc(int) int, x int) int {
	return cb(x)
}

func double(x int) int {
	return x * 2
}

func main() int {
	return callThrough(double, 21)
}
`)
	if strings.Contains(jm.ir, ".thunk") {
		t.Errorf("expected no synthesized thunk for a func-to-cfunc conversion, got:\n%s", jm.ir)
	}
	if !strings.Contains(jm.ir, "callThrough(ptr @double,") {
		t.Errorf("expected `double`'s own real address passed directly (`callThrough(ptr @double, ...)`), got:\n%s", jm.ir)
	}
	if got := jm.runInt32(t, "main"); got != 42 {
		t.Errorf("main() = %d, want 42 (double(21))", got)
	}
}

// TestCFuncCallJIT is TestTopLevelFuncToCFuncPassesRealAddressNotThunkIR's
// own real end-to-end proof, restated as a pure numeric assertion: calling
// through a cfunc-typed parameter, holding a converted top-level func,
// produces the correct result at run time - not just plausible-looking IR.
func TestCFuncCallJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func abs(x i32) i32

func callThrough(cb cfunc(i32) i32, x i32) i32 {
	return cb(x)
}

func main() int {
	var cb cfunc(i32) i32 = abs
	return callThrough(cb, -19)
}
`)
	if got := jm.runInt32(t, "main"); got != 19 {
		t.Errorf("main() = %d, want 19 (abs(-19))", got)
	}
}

// TestExternFuncToCFuncStructByValueCallCoercesIR covers genCFuncCall's own
// ABI coercion (ffi.go's externParamType/externReturnType, reused exactly
// as declareExternFuncSignature/genFuncCall already do for a direct extern
// call) applying identically when the same struct-by-value signature is
// called *indirectly* through a cfunc value instead - mirrors extern_test.go's
// own TestExternFuncStructByValueParamAndReturnCoercesToInteger. No JIT
// execution here - MakePoint is an unresolved, made-up extern symbol with
// no real backing implementation; the real end-to-end proof (linked
// against a genuine C callee) is cmd/llvmc's own
// TestBinary_AOT_LinkLibCFuncCallback instead.
func TestExternFuncToCFuncStructByValueCallCoercesIR(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

extern func MakePoint(x int, y int) Point

func callThrough(cb cfunc(int, int) Point, a int, b int) Point {
	return cb(a, b)
}

func main() int {
	p := callThrough(MakePoint, 3, 4)
	return p.x + p.y
}
`)
	if !strings.Contains(jm.ir, "callThrough(ptr @MakePoint,") {
		t.Errorf("expected `MakePoint`'s own real address passed directly, got:\n%s", jm.ir)
	}
	// The call itself must go through the same i64-coerced representation
	// externParamType/externReturnType already choose for MakePoint's own
	// direct-call declaration (see extern_test.go's
	// TestExternFuncStructByValueParamAndReturnCoercesToInteger) - a real
	// 2-field struct return would otherwise be silently misrepresented
	// against Windows x64's actual aggregate-return ABI (see ffi.go's own
	// doc comment).
	if !strings.Contains(jm.ir, "= call i64 %") {
		t.Errorf("expected the cfunc call itself to use the i64-coerced struct return, got:\n%s", jm.ir)
	}
}

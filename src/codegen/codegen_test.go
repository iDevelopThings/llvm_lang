package codegen

import (
	"sync"
	"syscall"
	"testing"

	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// jitInit performs LLVM's process-global JIT setup exactly once, mirroring
// third_party/go-llvm/executionengine_test.go's TestFactorial - every test
// in this file shares one process, and these calls aren't meant to run more
// than once per process.
var jitInit sync.Once

func initJIT() {
	jitInit.Do(func() {
		llvm.LinkInMCJIT()
		if err := llvm.InitializeNativeTarget(); err != nil {
			panic(err)
		}
		if err := llvm.InitializeNativeAsmPrinter(); err != nil {
			panic(err)
		}
	})
}

// compileSrc runs src through the full pipeline (parse -> resolve -> check ->
// codegen), failing the test if any stage reports a diagnostic - this
// package's Generate assumes a fully valid tree (see the package doc
// comment), so every test source here must actually be valid llvm_lang.
//
// The returned Module has NOT been handed to any ExecutionEngine - a caller
// that only wants to verify codegen (not JIT-execute) owns it outright and
// must call Dispose. A caller that wants to JIT-execute should use
// compileAndJIT instead (see its doc comment for why the two can't share a
// disposal path).
func compileSrc(t *testing.T, src string) *Module {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	info, rdiags := sema.Resolve(tree)
	if rdiags.HasErrors() {
		t.Fatalf("unexpected resolve errors for %q: %v", src, rdiags.All())
	}
	cdiags := sema.Check(tree, info)
	if cdiags.HasErrors() {
		t.Fatalf("unexpected check errors for %q: %v", src, cdiags.All())
	}

	mod, gdiags := Generate(tree, info, "test")
	if gdiags.HasErrors() {
		t.Fatalf("unexpected codegen errors for %q: %v", src, gdiags.All())
	}
	if err := llvm.VerifyModule(mod.LLVM, llvm.ReturnStatusAction); err != nil {
		t.Fatalf("module verification failed for %q: %v\n%s", src, err, mod.LLVM.String())
	}
	return mod
}

// compileSrcExpectCodegenError is compileSrc without asserting Generate
// itself found nothing - for the tests asserting a specific codegen-level
// diagnostic (see BLOCKERS.md). The module is discarded (never JIT-executed,
// never even disposed - the process exits at the end of `go test` either
// way, and these are single, short-lived test-only modules).
func compileSrcExpectCodegenError(t *testing.T, src string) *diag.Bag {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	info, rdiags := sema.Resolve(tree)
	if rdiags.HasErrors() {
		t.Fatalf("unexpected resolve errors for %q: %v", src, rdiags.All())
	}
	cdiags := sema.Check(tree, info)
	if cdiags.HasErrors() {
		t.Fatalf("unexpected check errors for %q: %v", src, cdiags.All())
	}
	_, gdiags := Generate(tree, info, "test")
	return gdiags
}

// jitModule is a compiled Module already handed to a live ExecutionEngine -
// see compileAndJIT.
type jitModule struct {
	mod    *Module
	engine llvm.ExecutionEngine
}

// compileAndJIT compiles src (see compileSrc) and hands the resulting module
// to a single ExecutionEngine shared for the whole test, so a test can call
// runInt32/runMainCapturingStdout as many times as it needs against the same
// module.
//
// This can't just be "compileSrc, then defer mod.Dispose()" the way a
// non-JIT test does: LLVMCreateExecutionEngineForModule takes ownership of
// the module, so disposing the *engine* already frees it - a later
// mod.Dispose() (or a second engine created for the same module) would
// double-free it, which is exactly what an earlier version of these tests
// did before this helper existed (a real crash, not a hypothetical one).
// t.Cleanup here disposes the engine (freeing the module with it) and only
// then the owning Context, in that order, exactly once.
func compileAndJIT(t *testing.T, src string) *jitModule {
	t.Helper()
	mod := compileSrc(t, src)
	initJIT()

	engine, err := llvm.NewExecutionEngine(mod.LLVM)
	if err != nil {
		t.Fatalf("NewExecutionEngine: %v", err)
	}
	t.Cleanup(func() {
		engine.Dispose()
		mod.Ctx.Dispose()
	})
	return &jitModule{
		mod:    mod,
		engine: engine,
	}
}

// address looks up name's JIT-compiled address, failing the test if it
// isn't in the module.
func (jm *jitModule) address(t *testing.T, name string) uintptr {
	t.Helper()
	addr := jm.engine.GetFunctionAddress(name)
	if addr == 0 {
		t.Fatalf("function %q not found in module", name)
	}
	return uintptr(addr)
}

// runInt32 JIT-executes the named function (declared with only i32/i1
// scalar parameters, and an i32/i1/void result) and returns its scalar
// result as an int32 - a bool result comes back as 0 or 1 either way, a void
// result as 0 (unused by the caller).
//
// This calls through the function's raw address via syscall.SyscallN,
// rather than ExecutionEngine.RunFunction/GenericValue (the approach
// go-llvm's own executionengine_test.go uses): MCJIT's RunFunction only
// supports a very small set of call shapes and aborts the whole process
// with "Full-featured argument passing not supported yet" for anything past
// that (a real fatal error hit while building this test suite against a
// 4-parameter function, not a hypothetical one - see BLOCKERS.md).
// syscall.SyscallN drives the exact same Windows x64 calling convention
// LLVM's own C calling convention lowers to on this target, so it's not a
// workaround so much as the more direct of the two ways to call a JIT'd
// function - it's also what GetFunctionAddress's own doc comment
// recommends.
func (jm *jitModule) runInt32(t *testing.T, name string, args ...int32) int32 {
	t.Helper()
	addr := jm.address(t, name)

	uargs := make([]uintptr, len(args))
	for i, a := range args {
		uargs[i] = uintptr(uint32(a))
	}
	r1, _, _ := syscall.SyscallN(addr, uargs...)
	return int32(uint32(r1))
}

// runBool is runInt32 for a bool (i1)-returning function specifically,
// masking the result down to its low bit before interpreting it.
//
// This masking matters, and isn't just defensive: LLVM's x86-64 lowering of
// an `i1` return value only guarantees bit 0 is meaningful, not the rest of
// the return register - any *LLVM-generated* caller already knows to read
// just that bit, but this test harness's raw syscall.SyscallN call doesn't
// go through LLVM's own calling-convention lowering, so it sees whatever
// garbage happens to be left in the upper bits (observed directly while
// building this suite: one bool-returning test function came back as
// 879755265 instead of 1). This is a test-harness-only concern, not a
// codegen bug - every consumer of a bool value *within* generated code
// (CreateCondBr, a bool-typed alloca, another i1-returning call) is
// produced and read by LLVM consistently either way.
// runInt64 is runInt32 for an i64-returning function - no masking needed on
// the way back (RAX already holds the full 64-bit result on this native x64
// ABI, unlike a narrower int32/bool result). Arguments passed through it are
// still plain i32/bool-width scalars, same as runInt32 - a real i64
// *argument* isn't needed by any test that uses this helper (every i64 value
// under test is embedded as a source-level literal/expression instead, the
// same established pattern this package's string-typed tests already use
// for the same underlying reason - see compileAndJIT's doc comment).
func (jm *jitModule) runInt64(t *testing.T, name string, args ...int32) int64 {
	t.Helper()
	addr := jm.address(t, name)

	uargs := make([]uintptr, len(args))
	for i, a := range args {
		uargs[i] = uintptr(uint32(a))
	}
	r1, _, _ := syscall.SyscallN(addr, uargs...)
	return int64(r1)
}

func (jm *jitModule) runBool(t *testing.T, name string, args ...int32) bool {
	t.Helper()
	addr := jm.address(t, name)

	uargs := make([]uintptr, len(args))
	for i, a := range args {
		uargs[i] = uintptr(uint32(a))
	}
	r1, _, _ := syscall.SyscallN(addr, uargs...)
	return r1&1 != 0
}

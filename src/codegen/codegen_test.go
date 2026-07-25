package codegen

import (
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// jitInit performs LLVM's process-global JIT setup exactly once - every test
// in this file shares one process, and these calls aren't meant to run more
// than once per process.
var jitInit sync.Once

func initJIT() {
	jitInit.Do(func() {
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
// The returned Module has NOT been handed to any LLJIT instance - a caller
// that only wants to verify codegen (not JIT-execute) owns it outright and
// must call Dispose. A caller that wants to JIT-execute should use
// compileAndJIT instead (see its doc comment for why the two can't share a
// disposal path).
func compileSrc(t *testing.T, src string) *Module {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src), false)
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
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src), false)
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

// bindMinGWMainThunk is cmd/llvmc/main.go's own helper of the same name,
// mirrored here exactly (see its doc comment for the full "why": a real
// MinGW/GCC ABI compatibility quirk LLVM's backend applies to any function
// literally named `main`, found empirically while switching this project's
// JIT engine to LLJIT - see DECISIONS.md's dated "JIT execution: LLJIT"
// entry). Every JIT-executing helper in this file (and imports_test.go's/
// multifile_test.go's own compileProgramAndJIT/compilePackageAndJIT) needs
// this exactly once per LLJIT instance, since every test source compiled
// through this package's own test helpers always declares a `func main()`
// of its own, per the language's top-level rules.
// Also binds __argc/__argv to harmless, always-valid process-local memory
// (testJITArgcSink/testJITArgvSink below) - see cmd/llvmc/main.go's own
// bindMinGWMainThunk (mirrored here) for the full "why": a test source
// calling args() declares these two as real extern globals (src/codegen/
// args.go), and this avoids ever needing them to resolve to anything
// meaningful, since no test here calls llvm_lang.args_init either.
func bindMinGWMainThunk(jit llvm.LLJIT) error {
	dg, err := llvm.NewDynamicLibrarySearchGeneratorForProcess(jit.GlobalPrefix())
	if err != nil {
		return err
	}
	jit.MainJITDylib().AddGenerator(dg)

	randAddr, err := jit.Lookup("rand")
	if err != nil {
		return err
	}

	mainName := jit.ExecutionSession().Intern("__main")
	defer mainName.Release()
	argcName := jit.ExecutionSession().Intern("__argc")
	defer argcName.Release()
	argvName := jit.ExecutionSession().Intern("__argv")
	defer argvName.Release()

	mu := llvm.AbsoluteSymbols([]llvm.AbsoluteSymbol{
		{
			Name: mainName,
			Value: llvm.EvaluatedSymbol{
				Address: randAddr,
				Flags:   llvm.SymbolFlags{Generic: llvm.SymbolFlagExported | llvm.SymbolFlagCallable},
			},
		},
		{
			Name: argcName,
			Value: llvm.EvaluatedSymbol{
				Address: uint64(uintptr(unsafe.Pointer(&testJITArgcSink))),
				Flags:   llvm.SymbolFlags{Generic: llvm.SymbolFlagExported},
			},
		},
		{
			Name: argvName,
			Value: llvm.EvaluatedSymbol{
				Address: uint64(uintptr(unsafe.Pointer(&testJITArgvSink))),
				Flags:   llvm.SymbolFlags{Generic: llvm.SymbolFlagExported},
			},
		},
	})
	return jit.MainJITDylib().Define(mu)
}

// testJITArgcSink/testJITArgvSink are the harmless, always-valid backing
// memory __argc/__argv are bound to under this file's own JIT test helpers -
// see bindMinGWMainThunk above.
var (
	testJITArgcSink int32
	testJITArgvSink uintptr
)

// jitModule is a compiled Module already added to a live LLJIT instance -
// see compileAndJIT.
type jitModule struct {
	mod *Module
	jit llvm.LLJIT
	// ir is mod.LLVM.String(), captured before mod was ever handed to jit -
	// see compileAndJIT's own doc comment for why a test wanting to inspect
	// the generated IR text must use this instead of calling
	// jm.mod.LLVM.String() itself.
	ir string
}

// compileAndJIT compiles src (see compileSrc) and adds the resulting module
// to a single LLJIT instance shared for the whole test, so a test can call
// runInt32/runMainCapturingStdout as many times as it needs against the same
// module.
//
// This can't just be "compileSrc, then defer mod.Dispose()" the way a
// non-JIT test does: wrapping mod.Ctx/mod.LLVM into a ThreadSafeContext/
// ThreadSafeModule and adding that to jit transfers ownership of both to the
// LLJIT instance - a later mod.Dispose() would double-free them, the same
// hazard an earlier, MCJIT-based version of this helper already hit for
// real (see DECISIONS.md's dated "JIT execution: LLJIT" entry). t.Cleanup
// here disposes only the LLJIT instance, which tears down the module and
// context together, in the correct order, in one call.
//
// The module's IR text is captured up front, before it's ever added to jit,
// for a test that wants to assert on the generated IR (e.g. that a specific
// printf call shape shows up) rather than (or in addition to) actually
// running it: unlike the legacy MCJIT ExecutionEngine (which kept the
// source Module intact for its whole lifetime), LLJIT's compile layer
// empties the original IR module out once it's been compiled to machine
// code - calling mod.LLVM.String() again *after* a Lookup/run through this
// jitModule returns an emptied-out module (just the header, verified
// directly: real content before, nothing but `; ModuleID = ...` and the
// datalayout line after) rather than the real generated IR, and one
// specific case of this was observed to crash outright, not just return the
// wrong thing. The IR text itself never changes after codegen - JIT
// compilation reads it to produce machine code, it doesn't rewrite the
// source module - so capturing it this early loses nothing.
func compileAndJIT(t *testing.T, src string) *jitModule {
	t.Helper()
	mod := compileSrc(t, src)
	ir := mod.LLVM.String()
	initJIT()

	jit, err := llvm.NewLLJIT(llvm.NewLLJITBuilder())
	if err != nil {
		t.Fatalf("NewLLJIT: %v", err)
	}

	if err := bindMinGWMainThunk(jit); err != nil {
		// mod isn't wrapped/handed to jit yet at this point (that happens
		// below, via AddLLVMIRModule) - still fully owned here.
		mod.Dispose()
		jit.Dispose()
		t.Fatalf("bindMinGWMainThunk: %v", err)
	}

	tsctx := llvm.NewThreadSafeContextFromContext(mod.Ctx)
	tsm := llvm.NewThreadSafeModule(mod.LLVM, tsctx)
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		jit.Dispose()
		t.Fatalf("AddLLVMIRModule: %v", err)
	}

	// Mirrors cmd/llvmc's own jitRunMain: a normal linked/loaded program's C
	// runtime would run @llvm.global_ctors (see CODEGEN.md's "Global var
	// initializers" section) before this test ever calls any function of its
	// own - LLJIT has no RunStaticConstructors-style call to trigger that
	// automatically, so this looks up llvm_lang.global_init directly by name
	// and calls it instead. Always safe: a module with no non-constant
	// globals has no such function to find at all (see buildGlobalInitFn), so
	// a failed Lookup here just means there was nothing to run.
	if initAddr, err := jit.Lookup("llvm_lang.global_init"); err == nil {
		syscall.SyscallN(uintptr(initAddr))
	}

	t.Cleanup(func() {
		if err := jit.Dispose(); err != nil {
			t.Errorf("LLJIT.Dispose: %v", err)
		}
	})
	return &jitModule{
		mod: mod,
		jit: jit,
		ir:  ir,
	}
}

// address looks up name's JIT-compiled address, failing the test if it
// isn't in the module.
func (jm *jitModule) address(t *testing.T, name string) uintptr {
	t.Helper()
	addr, err := jm.jit.Lookup(name)
	if err != nil {
		t.Fatalf("function %q not found in module: %v", name, err)
	}
	return uintptr(addr)
}

// runInt32 JIT-executes the named function (declared with only i32/i1
// scalar parameters, and an i32/i1/void result) and returns its scalar
// result as an int32 - a bool result comes back as 0 or 1 either way, a void
// result as 0 (unused by the caller).
//
// This calls through the function's raw address via syscall.SyscallN,
// rather than the legacy MCJIT ExecutionEngine's RunFunction/GenericValue
// (the approach go-llvm's own executionengine_test.go uses): MCJIT's
// RunFunction only supports a very small set of call shapes and aborts the
// whole process with "Full-featured argument passing not supported yet" for
// anything past that (a real fatal error hit while building this test suite
// against a 4-parameter function, not a hypothetical one - see BLOCKERS.md).
// LLJIT (the engine this package actually uses now - see DECISIONS.md's
// dated "JIT execution: LLJIT" entry) doesn't even have an equivalent of
// RunFunction/GenericValue to begin with: syscall.SyscallN driving the exact
// same Windows x64 calling convention LLVM's own C calling convention lowers
// to on this target isn't a workaround at all here, it's the only way to
// actually invoke a resolved address - see orcjit.go's own doc comment on
// LLJIT.Lookup.
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

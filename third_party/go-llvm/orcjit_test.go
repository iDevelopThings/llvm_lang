//go:build llvm22

//===- orcjit_test.go - Tests for orcjit -----------------------------------===//
//
// Part of the LLVM Project, under the Apache License v2.0 with LLVM Exceptions.
// See https://llvm.org/LICENSE.txt for license information.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
//===----------------------------------------------------------------------===//
//
// This file tests bindings for the ORC JIT (LLJIT) component.
//
//===----------------------------------------------------------------------===//

package llvm

import "testing"

// newHostLLJIT creates a default LLJIT instance targeting the host, failing
// t if construction fails, and registers a t.Cleanup to dispose of it -
// every test in this file needs one of these and nothing more specific, so
// this factors out the repeated four lines rather than than duplicating them
// per test.
func newHostLLJIT(t *testing.T) LLJIT {
	t.Helper()
	jit, err := NewLLJIT(NewLLJITBuilder())
	if err != nil {
		t.Fatalf("NewLLJIT: %v", err)
	}
	t.Cleanup(func() {
		if err := jit.Dispose(); err != nil {
			t.Errorf("LLJIT.Dispose: %v", err)
		}
	})
	return jit
}

// TestLLJITFactorial is executionengine_test.go's own TestFactorial, run
// through LLJIT instead of the legacy MCJIT-based ExecutionEngine - the same
// recursive `fac` function, JIT-compiled and called through its resolved
// address (see callI32FnI32 in orcjit_call.go) rather than
// RunFunction/GenericValue, since LLJIT has no equivalent of those at all.
func TestLLJITFactorial(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	ctx := NewContext()
	mod := ctx.NewModule("fac_module")

	fac_args := []Type{ctx.Int32Type()}
	fac_type := FunctionType(ctx.Int32Type(), fac_args, false)
	fac := AddFunction(mod, "fac", fac_type)
	fac.SetFunctionCallConv(CCallConv)
	n := fac.Param(0)

	entry := AddBasicBlock(fac, "entry")
	iftrue := AddBasicBlock(fac, "iftrue")
	iffalse := AddBasicBlock(fac, "iffalse")
	end := AddBasicBlock(fac, "end")

	builder := ctx.NewBuilder()
	defer builder.Dispose()

	builder.SetInsertPointAtEnd(entry)
	If := builder.CreateICmp(IntEQ, n, ConstInt(ctx.Int32Type(), 0, false), "cmptmp")
	builder.CreateCondBr(If, iftrue, iffalse)

	builder.SetInsertPointAtEnd(iftrue)
	res_iftrue := ConstInt(ctx.Int32Type(), 1, false)
	builder.CreateBr(end)

	builder.SetInsertPointAtEnd(iffalse)
	n_minus := builder.CreateSub(n, ConstInt(ctx.Int32Type(), 1, false), "subtmp")
	call_fac_args := []Value{n_minus}
	call_fac := builder.CreateCall(fac_type, fac, call_fac_args, "calltmp")
	res_iffalse := builder.CreateMul(n, call_fac, "multmp")
	builder.CreateBr(end)

	builder.SetInsertPointAtEnd(end)
	res := builder.CreatePHI(ctx.Int32Type(), "result")
	phi_vals := []Value{res_iftrue, res_iffalse}
	phi_blocks := []BasicBlock{iftrue, iffalse}
	res.AddIncoming(phi_vals, phi_blocks)
	builder.CreateRet(res)

	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module: %s", err)
	}

	jit := newHostLLJIT(t)

	// ThreadSafeContext ownership is shared (see its own doc comment): tsm
	// keeps the underlying context data alive on its own once created, so
	// disposing tsctx immediately afterward - rather than holding onto it -
	// is exactly the intended usage, not a race.
	tsctx := NewThreadSafeContextFromContext(ctx)
	tsm := NewThreadSafeModule(mod, tsctx)
	tsctx.Dispose()

	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		t.Fatalf("AddLLVMIRModule: %v", err)
	}

	addr, err := jit.Lookup("fac")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	var fac10 int32 = 10 * 9 * 8 * 7 * 6 * 5 * 4 * 3 * 2 * 1
	if got := callI32FnI32(addr, 10); got != fac10 {
		t.Errorf("fac(10): expected %d, got %d", fac10, got)
	}
}

// TestLLJITLookupMissingSymbol checks that looking up a symbol no added
// module defines - and that no attached generator can supply either -
// reports a real error rather than a zero/garbage address.
func TestLLJITLookupMissingSymbol(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	jit := newHostLLJIT(t)

	if addr, err := jit.Lookup("this_symbol_does_not_exist_anywhere"); err == nil {
		t.Fatalf("expected an error looking up a nonexistent symbol, got address %#x", addr)
	}
}

// TestLLJITProcessSymbolGenerator checks that a JIT'd module can call an
// ordinary external libc function it only declares (never defines) once
// NewDynamicLibrarySearchGeneratorForProcess's generator is attached to the
// JITDylib doing the lookup - the ORC replacement for
// ExecutionEngine.AddGlobalMapping's manual per-symbol mapping under the
// older MCJIT API.
func TestLLJITProcessSymbolGenerator(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	ctx := NewContext()
	mod := ctx.NewModule("process_symbols")
	i32 := ctx.Int32Type()

	// `abs` is declared, never defined in this module - it must resolve
	// against the host process's own libc.
	absType := FunctionType(i32, []Type{i32}, false)
	absFn := AddFunction(mod, "abs", absType)

	callerType := FunctionType(i32, []Type{i32}, false)
	caller := AddFunction(mod, "call_abs", callerType)
	caller.SetFunctionCallConv(CCallConv)
	entry := AddBasicBlock(caller, "entry")

	b := ctx.NewBuilder()
	defer b.Dispose()
	b.SetInsertPointAtEnd(entry)
	r := b.CreateCall(absType, absFn, []Value{caller.Param(0)}, "r")
	b.CreateRet(r)

	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module: %s", err)
	}

	jit := newHostLLJIT(t)

	dg, err := NewDynamicLibrarySearchGeneratorForProcess(jit.GlobalPrefix())
	if err != nil {
		t.Fatalf("NewDynamicLibrarySearchGeneratorForProcess: %v", err)
	}
	jit.MainJITDylib().AddGenerator(dg)

	tsctx := NewThreadSafeContextFromContext(ctx)
	tsm := NewThreadSafeModule(mod, tsctx)
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		t.Fatalf("AddLLVMIRModule: %v", err)
	}

	addr, err := jit.Lookup("call_abs")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if got := callI32FnI32(addr, -7); got != 7 {
		t.Errorf("call_abs(-7): expected 7, got %d", got)
	}
}

// TestLLJITMultipleModulesSharedDylib checks a real ORC advantage a single
// MCJIT ExecutionEngine never had: two independently-added modules, sharing
// one JITDylib, can call into each other - moduleB's doubleHelper calls
// moduleA's helper by symbol name alone, resolved purely because both
// modules were added to the same jit.MainJITDylib().
func TestLLJITMultipleModulesSharedDylib(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	jit := newHostLLJIT(t)

	ctxA := NewContext()
	modA := ctxA.NewModule("module_a")
	i32A := ctxA.Int32Type()
	helperType := FunctionType(i32A, nil, false)
	helper := AddFunction(modA, "helper", helperType)
	helper.SetFunctionCallConv(CCallConv)
	entryA := AddBasicBlock(helper, "entry")
	bA := ctxA.NewBuilder()
	bA.SetInsertPointAtEnd(entryA)
	bA.CreateRet(ConstInt(i32A, 21, false))
	bA.Dispose()

	if err := VerifyModule(modA, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module A: %s", err)
	}

	ctxB := NewContext()
	modB := ctxB.NewModule("module_b")
	i32B := ctxB.Int32Type()
	helperDecl := AddFunction(modB, "helper", FunctionType(i32B, nil, false))
	double := AddFunction(modB, "doubleHelper", FunctionType(i32B, nil, false))
	double.SetFunctionCallConv(CCallConv)
	entryB := AddBasicBlock(double, "entry")
	bB := ctxB.NewBuilder()
	bB.SetInsertPointAtEnd(entryB)
	r := bB.CreateCall(FunctionType(i32B, nil, false), helperDecl, nil, "r")
	bB.CreateRet(bB.CreateMul(r, ConstInt(i32B, 2, false), "doubled"))
	bB.Dispose()

	if err := VerifyModule(modB, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module B: %s", err)
	}

	tsmA := NewThreadSafeModule(modA, NewThreadSafeContextFromContext(ctxA))
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsmA); err != nil {
		t.Fatalf("AddLLVMIRModule (A): %v", err)
	}

	tsmB := NewThreadSafeModule(modB, NewThreadSafeContextFromContext(ctxB))
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsmB); err != nil {
		t.Fatalf("AddLLVMIRModule (B): %v", err)
	}

	addr, err := jit.Lookup("doubleHelper")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if got := callI32FnVoid(addr); got != 42 {
		t.Errorf("doubleHelper(): expected 42, got %d", got)
	}
}

// TestLLJITExplicitTargetMachineBuilder checks the non-default construction
// path - building a JITTargetMachineBuilder from an explicit TargetMachine
// (target.go's Target.CreateTargetMachine) instead of DetectHost - still
// produces a working LLJIT instance targeting the expected triple.
func TestLLJITExplicitTargetMachineBuilder(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	triple := DefaultTargetTriple()
	targ, err := GetTargetFromTriple(triple)
	if err != nil {
		t.Fatalf("GetTargetFromTriple: %v", err)
	}
	tm := targ.CreateTargetMachine(triple, "", "", CodeGenLevelDefault, RelocDefault, CodeModelDefault)

	jtmb := NewJITTargetMachineBuilderFromTargetMachine(tm)

	b := NewLLJITBuilder()
	b.SetJITTargetMachineBuilder(jtmb)

	jit, err := NewLLJIT(b)
	if err != nil {
		t.Fatalf("NewLLJIT: %v", err)
	}
	defer func() {
		if err := jit.Dispose(); err != nil {
			t.Errorf("LLJIT.Dispose: %v", err)
		}
	}()

	if got := jit.TripleString(); got != triple {
		t.Errorf("expected triple %q, got %q", triple, got)
	}
}

// TestJITTargetMachineBuilderTargetTriple checks the TargetTriple/
// SetTargetTriple accessor pair round-trips correctly, independent of
// DetectHost vs. CreateFromTargetMachine construction.
func TestJITTargetMachineBuilderTargetTriple(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	jtmb, err := NewJITTargetMachineBuilderDetectHost()
	if err != nil {
		t.Fatalf("NewJITTargetMachineBuilderDetectHost: %v", err)
	}
	defer jtmb.Dispose()

	triple := DefaultTargetTriple()
	if got := jtmb.TargetTriple(); got != triple {
		t.Errorf("expected detected triple %q, got %q", triple, got)
	}

	jtmb.SetTargetTriple(triple)
	if got := jtmb.TargetTriple(); got != triple {
		t.Errorf("after SetTargetTriple(%q): got %q", triple, got)
	}
}

// TestLLJITResourceTrackerUnload checks ORC's headline advantage over the
// legacy MCJIT ExecutionEngine, which this package's other JIT path
// (executionengine.go) has no equivalent of at all: code added under a
// specific ResourceTracker can be unloaded from a still-running LLJIT
// instance, independently of anything else added to the same JITDylib, via
// ResourceTracker.Remove - after which it's no longer resolvable.
func TestLLJITResourceTrackerUnload(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	jit := newHostLLJIT(t)

	ctx := NewContext()
	mod := ctx.NewModule("unload_module")
	i32 := ctx.Int32Type()
	fn := AddFunction(mod, "answer", FunctionType(i32, nil, false))
	fn.SetFunctionCallConv(CCallConv)
	entry := AddBasicBlock(fn, "entry")
	b := ctx.NewBuilder()
	b.SetInsertPointAtEnd(entry)
	b.CreateRet(ConstInt(i32, 42, false))
	b.Dispose()

	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module: %s", err)
	}

	rt := jit.MainJITDylib().CreateResourceTracker()
	defer rt.Release()

	tsm := NewThreadSafeModule(mod, NewThreadSafeContextFromContext(ctx))
	if err := jit.AddLLVMIRModuleWithRT(rt, tsm); err != nil {
		t.Fatalf("AddLLVMIRModuleWithRT: %v", err)
	}

	addr, err := jit.Lookup("answer")
	if err != nil {
		t.Fatalf("Lookup before unload: %v", err)
	}
	if got := callI32FnVoid(addr); got != 42 {
		t.Fatalf("answer(): expected 42, got %d", got)
	}

	if err := rt.Remove(); err != nil {
		t.Fatalf("ResourceTracker.Remove: %v", err)
	}

	if addr, err := jit.Lookup("answer"); err == nil {
		t.Fatalf("expected Lookup to fail after unload, got address %#x", addr)
	}
}

// TestLLJITExecutionSessionCreateJITDylib checks that additional JITDylibs
// (beyond jit.MainJITDylib) can be created and looked back up by name - the
// entry point for isolating groups of modules from each other within a
// single LLJIT instance.
func TestLLJITExecutionSessionCreateJITDylib(t *testing.T) {
	jit := newHostLLJIT(t)
	es := jit.ExecutionSession()

	jd, err := es.CreateJITDylib("extra")
	if err != nil {
		t.Fatalf("CreateJITDylib: %v", err)
	}
	if jd.C == nil {
		t.Fatal("expected a non-nil JITDylib")
	}

	if found := es.JITDylibByName("extra"); found.C != jd.C {
		t.Error("JITDylibByName: expected to find the dylib just created")
	}

	if found := es.JITDylibByName("does_not_exist_either"); found.C != nil {
		t.Error("JITDylibByName for a nonexistent name: expected nil, got a dylib")
	}

	bare := es.CreateBareJITDylib("extra_bare")
	if bare.C == nil {
		t.Fatal("expected a non-nil bare JITDylib")
	}
}

// TestLLJITPathGeneratorsErrorOnMissingFile checks the error path for both
// path-based symbol generators - the same shape of coverage
// TestLLJITLookupMissingSymbol gives NewDynamicLibrarySearchGeneratorForProcess's
// sibling constructors, without depending on any specific library actually
// existing on the test machine.
func TestLLJITPathGeneratorsErrorOnMissingFile(t *testing.T) {
	jit := newHostLLJIT(t)

	if _, err := NewDynamicLibrarySearchGeneratorForPath("/no/such/library.so", jit.GlobalPrefix()); err == nil {
		t.Error("expected an error for a nonexistent dynamic library path")
	}

	if _, err := NewStaticLibrarySearchGeneratorForPath(jit.ObjLinkingLayer(), "/no/such/library.a"); err == nil {
		t.Error("expected an error for a nonexistent static library path")
	}
}

// TestExecutionSessionIntern checks that Intern round-trips a name as-is -
// unlike LLJIT.MangleAndIntern (exercised by TestLLJITAbsoluteSymbols
// instead, where the mangling actually matters), Intern is documented to
// perform no linker mangling at all.
func TestExecutionSessionIntern(t *testing.T) {
	jit := newHostLLJIT(t)

	entry := jit.ExecutionSession().Intern("some_unmangled_name")
	defer entry.Release()
	if got := entry.String(); got != "some_unmangled_name" {
		t.Errorf("expected %q, got %q", "some_unmangled_name", got)
	}
}

// TestLLJITAbsoluteSymbols checks the other way to expose a host function to
// JIT'd code (besides NewDynamicLibrarySearchGeneratorForProcess, covered by
// TestLLJITProcessSymbolGenerator): binding a chosen name directly to a raw
// address via AbsoluteSymbols/JITDylib.Define, with no dlsym-style
// resolution involved at all. This module declares `abs` and calls it, the
// same shape as TestLLJITProcessSymbolGenerator, but here `abs`'s address
// is supplied explicitly (via the host's own `abs`, see absAddress in
// orcjit_call.go) rather than discovered by the process generator.
func TestLLJITAbsoluteSymbols(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	ctx := NewContext()
	mod := ctx.NewModule("absolute_symbols")
	i32 := ctx.Int32Type()

	absType := FunctionType(i32, []Type{i32}, false)
	absDecl := AddFunction(mod, "abs", absType)

	callerType := FunctionType(i32, []Type{i32}, false)
	caller := AddFunction(mod, "call_abs_absolute", callerType)
	caller.SetFunctionCallConv(CCallConv)
	entry := AddBasicBlock(caller, "entry")

	b := ctx.NewBuilder()
	defer b.Dispose()
	b.SetInsertPointAtEnd(entry)
	r := b.CreateCall(absType, absDecl, []Value{caller.Param(0)}, "r")
	b.CreateRet(r)

	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module: %s", err)
	}

	jit := newHostLLJIT(t)

	name := jit.MangleAndIntern("abs")
	mu := AbsoluteSymbols([]AbsoluteSymbol{
		{
			Name: name,
			Value: EvaluatedSymbol{
				Address: absAddress(),
				Flags:   SymbolFlags{Generic: SymbolFlagExported | SymbolFlagCallable},
			},
		},
	})
	name.Release()

	if err := jit.MainJITDylib().Define(mu); err != nil {
		t.Fatalf("JITDylib.Define: %v", err)
	}

	tsm := NewThreadSafeModule(mod, NewThreadSafeContextFromContext(ctx))
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		t.Fatalf("AddLLVMIRModule: %v", err)
	}

	addr, err := jit.Lookup("call_abs_absolute")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	if got := callI32FnI32(addr, -9); got != 9 {
		t.Errorf("call_abs_absolute(-9): expected 9, got %d", got)
	}
}

// TestExecutionSessionLookup checks the general form of Lookup - unlike
// LLJIT.Lookup (main JITDylib only), this can search an arbitrary set of
// JITDylibs, including ones created via ExecutionSession.CreateJITDylib
// rather than jit.MainJITDylib - and that a symbol only ever added to a
// non-main JITDylib is genuinely invisible to LLJIT.Lookup, confirming the
// two aren't just doing the same search under different names.
func TestExecutionSessionLookup(t *testing.T) {
	InitializeNativeTarget()
	InitializeNativeAsmPrinter()

	jit := newHostLLJIT(t)
	es := jit.ExecutionSession()

	extraJD, err := es.CreateJITDylib("extra_for_lookup")
	if err != nil {
		t.Fatalf("CreateJITDylib: %v", err)
	}

	ctx := NewContext()
	mod := ctx.NewModule("extra_module")
	i32 := ctx.Int32Type()
	fn := AddFunction(mod, "extra_answer", FunctionType(i32, nil, false))
	fn.SetFunctionCallConv(CCallConv)
	entry := AddBasicBlock(fn, "entry")
	b := ctx.NewBuilder()
	b.SetInsertPointAtEnd(entry)
	b.CreateRet(ConstInt(i32, 7, false))
	b.Dispose()

	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("Error verifying module: %s", err)
	}

	tsm := NewThreadSafeModule(mod, NewThreadSafeContextFromContext(ctx))
	if err := jit.AddLLVMIRModule(extraJD, tsm); err != nil {
		t.Fatalf("AddLLVMIRModule into extra JITDylib: %v", err)
	}

	if addr, err := jit.Lookup("extra_answer"); err == nil {
		t.Fatalf("expected LLJIT.Lookup (main dylib only) to miss extra_answer, got address %#x", addr)
	}

	addr, err := es.Lookup([]JITDylib{extraJD}, "extra_answer")
	if err != nil {
		t.Fatalf("ExecutionSession.Lookup: %v", err)
	}
	if got := callI32FnVoid(addr); got != 7 {
		t.Errorf("extra_answer(): expected 7, got %d", got)
	}

	if addr, err := es.Lookup([]JITDylib{extraJD}, "does_not_exist_in_any_dylib"); err == nil {
		t.Fatalf("expected an error looking up a nonexistent symbol, got address %#x", addr)
	}
}

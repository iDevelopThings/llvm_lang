//go:build llvm22

//===- orcjit.go - Bindings for ORC JIT (LLJIT) ---------------------------===//
//
// Part of the LLVM Project, under the Apache License v2.0 with LLVM Exceptions.
// See https://llvm.org/LICENSE.txt for license information.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
//===----------------------------------------------------------------------===//
//
// This file defines bindings for the ORC JIT component (llvm-c/Orc.h,
// llvm-c/LLJIT.h and llvm-c/OrcEE.h): LLJIT, the "easy to use" ORCv2-based
// JIT that replaces the legacy MCJIT ExecutionEngine (see executionengine.go)
// for new code - MCJIT is unmaintained upstream and LLJIT is what LLVM itself
// recommends for new JIT use cases (see
// https://llvm.org/docs/ORCv2.html#transitioning-from-orcv1-to-orcv2; ORCv1's
// own separate C API was removed from LLVM entirely some releases ago, so
// Orc.h/LLJIT.h/OrcEE.h are ORCv2 unconditionally - there is no version
// selection to make there).
//
// This binds LLVMOrcCreateNewThreadSafeContextFromLLVMContext, which lets a
// ThreadSafeContext wrap a Context this package already created (rather than
// forcing callers to build their whole module inside a JIT-owned context
// from scratch). That function was only added in LLVM 21
// (llvm/llvm-project@ade4d494c53), so this file is gated to llvm22 for now,
// following the same pattern as switch_llvm22.go/switch_pre22.go for a prior
// llvm22-only API split. A pre-21 fallback (using
// LLVMOrcCreateNewThreadSafeContext's own fresh context instead) is possible
// but intentionally left for a follow-up - see
// https://github.com/tinygo-org/go-llvm/issues/66.
//
// Coverage is deliberately scoped to what an ordinary LLJIT consumer needs:
// building/adding modules and object files, resolving symbols, multiple
// JITDylibs (ExecutionSession), unloading previously-added code
// (ResourceTracker), and the built-in symbol generators (process/path/static
// library). Left out, as a distinct and much larger follow-up: the building
// blocks for implementing a *custom* on-demand/lazy-compiling JIT layer on
// top of ORC - MaterializationResponsibility/MaterializationUnit,
// IndirectStubsManager, LazyCallThroughManager/LazyReexports - along with
// the IRTransformLayer/ObjectTransformLayer callback hooks, direct
// SymbolStringPool manipulation, OrcEE's alternate object-linking-layer and
// JITEventListener (gdb/perf) wiring, and ThreadSafeModule.WithModuleDo.
// None of these are needed to use LLJIT itself - they're for building
// something LLJIT-like from scratch.
//
//===----------------------------------------------------------------------===//

package llvm

/*
#include "llvm-c/Core.h"
#include "llvm-c/Orc.h"
#include "llvm-c/LLJIT.h"
#include "llvm-c/OrcEE.h"
#include "llvm-c/Error.h"
#include <stdlib.h>
*/
import "C"
import (
	"errors"
	"unsafe"
)

// orcError converts a LLVMErrorRef into a Go error, consuming it in the
// process (via LLVMGetErrorMessage/LLVMDisposeErrorMessage) - every ORC C API
// function that can fail returns one of these instead of the
// bool-plus-out-parameter pattern executionengine.go's older APIs use, and
// LLVM aborts the process if a non-nil LLVMErrorRef is ever dropped without
// being consumed, so every call site below routes its result through this
// rather than checking/discarding it directly.
func orcError(err C.LLVMErrorRef) error {
	if err == nil {
		return nil
	}
	cstr := C.LLVMGetErrorMessage(err)
	gstr := C.GoString(cstr)
	C.LLVMDisposeErrorMessage(cstr)
	return errors.New(gstr)
}

//-------------------------------------------------------------------------
// llvm.ThreadSafeContext / llvm.ThreadSafeModule
//-------------------------------------------------------------------------

// ThreadSafeContext is a reference-counted wrapper around a Context, letting
// it be shared safely across the internal threads an LLJIT instance may use.
type ThreadSafeContext struct {
	C C.LLVMOrcThreadSafeContextRef
}

// NewThreadSafeContext creates a ThreadSafeContext wrapping a brand new
// Context, owned by the ThreadSafeContext from this point on.
func NewThreadSafeContext() (tsctx ThreadSafeContext) {
	tsctx.C = C.LLVMOrcCreateNewThreadSafeContext()
	return
}

// NewThreadSafeContextFromContext creates a ThreadSafeContext wrapping ctx,
// an already-existing Context. Ownership of ctx transfers to the returned
// ThreadSafeContext: ctx must not be used or disposed of directly afterward,
// and must not already be associated with any other ThreadSafeContext.
func NewThreadSafeContextFromContext(ctx Context) (tsctx ThreadSafeContext) {
	tsctx.C = C.LLVMOrcCreateNewThreadSafeContextFromLLVMContext(ctx.C)
	return
}

// Dispose releases this reference to the underlying ThreadSafeContext data.
// This is safe to call as soon as a ThreadSafeModule has been created from
// it (see NewThreadSafeModule) - the underlying data is kept alive by that
// ThreadSafeModule's own reference for as long as it's actually needed.
func (tsctx ThreadSafeContext) Dispose() {
	C.LLVMOrcDisposeThreadSafeContext(tsctx.C)
}

// ThreadSafeModule is a reference-counted wrapper around a Module, pairing
// it with the ThreadSafeContext it was built in.
type ThreadSafeModule struct {
	C C.LLVMOrcThreadSafeModuleRef
}

// NewThreadSafeModule wraps m in a ThreadSafeModule alongside tsctx - the
// Context m belongs to. This takes ownership of m: it must not be disposed
// of or referenced again afterward, whether or not the returned
// ThreadSafeModule is ever added to an LLJIT (see LLJIT.AddLLVMIRModule).
func NewThreadSafeModule(m Module, tsctx ThreadSafeContext) (tsm ThreadSafeModule) {
	tsm.C = C.LLVMOrcCreateNewThreadSafeModule(m.C, tsctx.C)
	return
}

// Dispose releases tsm. This must only be called if tsm was never added to
// an LLJIT (see LLJIT.AddLLVMIRModule) - adding it transfers ownership to
// the LLJIT instance, which disposes of it itself.
func (tsm ThreadSafeModule) Dispose() {
	C.LLVMOrcDisposeThreadSafeModule(tsm.C)
}

//-------------------------------------------------------------------------
// llvm.JITTargetMachineBuilder
//-------------------------------------------------------------------------

// JITTargetMachineBuilder describes the target an LLJIT instance should
// generate code for.
type JITTargetMachineBuilder struct {
	C C.LLVMOrcJITTargetMachineBuilderRef
}

// NewJITTargetMachineBuilderDetectHost creates a JITTargetMachineBuilder by
// inspecting the host process's own target (the common case: JIT-compiling
// for the same machine that's running the compiler). This is what
// NewLLJITBuilder uses by default if LLJITBuilder.SetJITTargetMachineBuilder
// is never called.
func NewJITTargetMachineBuilderDetectHost() (jtmb JITTargetMachineBuilder, err error) {
	cerr := C.LLVMOrcJITTargetMachineBuilderDetectHost(&jtmb.C)
	err = orcError(cerr)
	return
}

// NewJITTargetMachineBuilderFromTargetMachine creates a JITTargetMachineBuilder
// from an existing TargetMachine template (see target.go's
// Target.CreateTargetMachine) rather than detecting the host - useful for
// cross-JIT-compiling for a target other than the host, or for reusing
// tuning/feature flags already computed for a TargetMachine elsewhere. This
// takes ownership of tm and destroys it: tm must not be used or disposed of
// afterward.
func NewJITTargetMachineBuilderFromTargetMachine(tm TargetMachine) (jtmb JITTargetMachineBuilder) {
	jtmb.C = C.LLVMOrcJITTargetMachineBuilderCreateFromTargetMachine(tm.C)
	return
}

// Dispose releases jtmb. This must only be called if jtmb was never passed
// to LLJITBuilder.SetJITTargetMachineBuilder - that call takes ownership of
// it instead.
func (jtmb JITTargetMachineBuilder) Dispose() {
	C.LLVMOrcDisposeJITTargetMachineBuilder(jtmb.C)
}

// TargetTriple returns jtmb's configured target triple.
func (jtmb JITTargetMachineBuilder) TargetTriple() string {
	cstr := C.LLVMOrcJITTargetMachineBuilderGetTargetTriple(jtmb.C)
	gstr := C.GoString(cstr)
	C.LLVMDisposeMessage(cstr)
	return gstr
}

// SetTargetTriple sets jtmb's target triple to triple.
func (jtmb JITTargetMachineBuilder) SetTargetTriple(triple string) {
	ctriple := C.CString(triple)
	defer C.free(unsafe.Pointer(ctriple))
	C.LLVMOrcJITTargetMachineBuilderSetTargetTriple(jtmb.C, ctriple)
}

//-------------------------------------------------------------------------
// llvm.ExecutionSession
//-------------------------------------------------------------------------

// ExecutionSession owns and coordinates every JITDylib, and all the state
// shared between them, for a given LLJIT instance (see LLJIT.ExecutionSession).
type ExecutionSession struct {
	C C.LLVMOrcExecutionSessionRef
}

// CreateBareJITDylib creates a new, empty JITDylib named name in es - unlike
// CreateJITDylib, this never installs any platform symbols into it (e.g. the
// standard library interposes a Platform would otherwise set up): the caller
// is responsible for populating it entirely (typically via
// JITDylib.AddGenerator). The caller is responsible for ensuring name is
// unique - see ExecutionSession.JITDylibByName.
func (es ExecutionSession) CreateBareJITDylib(name string) (jd JITDylib) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	jd.C = C.LLVMOrcExecutionSessionCreateBareJITDylib(es.C, cname)
	return
}

// CreateJITDylib creates a new JITDylib named name in es. If es has a
// Platform attached, it installs that platform's standard symbols into the
// new JITDylib (e.g. standard library interposes) - with no Platform
// attached, this behaves exactly like CreateBareJITDylib and always
// succeeds. The caller is responsible for ensuring name is unique - see
// ExecutionSession.JITDylibByName.
func (es ExecutionSession) CreateJITDylib(name string) (jd JITDylib, err error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	cerr := C.LLVMOrcExecutionSessionCreateJITDylib(es.C, &jd.C, cname)
	err = orcError(cerr)
	return
}

// JITDylibByName returns the JITDylib named name in es, or the zero
// JITDylib if no such JITDylib exists.
func (es ExecutionSession) JITDylibByName(name string) (jd JITDylib) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	jd.C = C.LLVMOrcExecutionSessionGetJITDylibByName(es.C, cname)
	return
}

// Intern interns name in es's SymbolStringPool and returns a reference to
// it, incrementing its ref-count - the caller must call
// SymbolStringPoolEntry.Release once it's done with the result. Unlike
// LLJIT.MangleAndIntern, this performs no linker mangling on name: it's
// the right call for a name that's already mangled (or never needs to be),
// e.g. one being handed straight to AbsoluteSymbols/JITDylib.Define.
func (es ExecutionSession) Intern(name string) (entry SymbolStringPoolEntry) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	entry.C = C.LLVMOrcExecutionSessionIntern(es.C, cname)
	return
}

//-------------------------------------------------------------------------
// llvm.SymbolStringPoolEntry / llvm.EvaluatedSymbol
//-------------------------------------------------------------------------

// SymbolStringPoolEntry is a reference-counted, uniqued entry in an
// ExecutionSession's SymbolStringPool - the interned form of a symbol name
// every ORC API that deals in symbol names (AbsoluteSymbols,
// ExecutionSession.Lookup, ...) actually uses internally. Obtained via
// ExecutionSession.Intern or LLJIT.MangleAndIntern.
type SymbolStringPoolEntry struct {
	C C.LLVMOrcSymbolStringPoolEntryRef
}

// Retain increments entry's ref-count.
func (entry SymbolStringPoolEntry) Retain() {
	C.LLVMOrcRetainSymbolStringPoolEntry(entry.C)
}

// Release decrements entry's ref-count. Every SymbolStringPoolEntry
// obtained from this package (directly, or implicitly retained on the
// caller's behalf - see AbsoluteSymbols) must eventually have Release
// called on it exactly as many times as it was obtained/retained.
func (entry SymbolStringPoolEntry) Release() {
	C.LLVMOrcReleaseSymbolStringPoolEntry(entry.C)
}

// String returns entry's underlying string.
func (entry SymbolStringPoolEntry) String() string {
	return C.GoString(C.LLVMOrcSymbolStringPoolEntryStr(entry.C))
}

// SymbolGenericFlags are the linkage flags for a symbol definition that
// apply on every target - see SymbolFlags.
type SymbolGenericFlags uint8

const (
	SymbolFlagExported                       SymbolGenericFlags = C.LLVMJITSymbolGenericFlagsExported
	SymbolFlagWeak                           SymbolGenericFlags = C.LLVMJITSymbolGenericFlagsWeak
	SymbolFlagCallable                       SymbolGenericFlags = C.LLVMJITSymbolGenericFlagsCallable
	SymbolFlagMaterializationSideEffectsOnly SymbolGenericFlags = C.LLVMJITSymbolGenericFlagsMaterializationSideEffectsOnly
)

// SymbolFlags describes the linkage of a symbol definition - see
// EvaluatedSymbol.
type SymbolFlags struct {
	// Generic is a bitwise combination of the SymbolFlag* constants.
	Generic SymbolGenericFlags
	// Target holds target-specific flags this package doesn't otherwise
	// interpret - 0 for the common case.
	Target uint8
}

// EvaluatedSymbol pairs a resolved address with its linkage flags - see
// AbsoluteSymbol.
type EvaluatedSymbol struct {
	Address uint64
	Flags   SymbolFlags
}

//-------------------------------------------------------------------------
// llvm.MaterializationUnit
//-------------------------------------------------------------------------

// MaterializationUnit describes how to provide definitions for a set of
// symbols - obtained from a constructor like AbsoluteSymbols, and consumed
// by JITDylib.Define.
type MaterializationUnit struct {
	C C.LLVMOrcMaterializationUnitRef
}

// Dispose releases mu. This must only be called if mu was never passed to
// JITDylib.Define, or that call itself failed - a successful Define call
// takes ownership of mu instead.
func (mu MaterializationUnit) Dispose() {
	C.LLVMOrcDisposeMaterializationUnit(mu.C)
}

// AbsoluteSymbol pairs an interned symbol name with the fixed address (and
// linkage flags) it should resolve to - see AbsoluteSymbols.
type AbsoluteSymbol struct {
	Name  SymbolStringPoolEntry
	Value EvaluatedSymbol
}

// AbsoluteSymbols creates a MaterializationUnit that defines each of syms as
// pointing directly at its given address - the way to expose an arbitrary
// host function pointer (e.g. a cgo-exported runtime helper of the caller's
// own) to JIT'd code under a chosen name, as opposed to
// NewDynamicLibrarySearchGeneratorForProcess's coarser "reflect whatever the
// process already exports" approach. Pass the result to JITDylib.Define to
// actually install it.
//
// This retains each syms[i].Name once on the caller's behalf (matching the
// C API's own documented contract): the caller's own reference (e.g. from
// ExecutionSession.Intern) remains theirs to Release as usual.
func AbsoluteSymbols(syms []AbsoluteSymbol) (mu MaterializationUnit) {
	if len(syms) == 0 {
		mu.C = C.LLVMOrcAbsoluteSymbols(nil, 0)
		return
	}
	cpairs := make([]C.LLVMOrcCSymbolMapPair, len(syms))
	for i, s := range syms {
		s.Name.Retain()
		cpairs[i].Name = s.Name.C
		cpairs[i].Sym.Address = C.LLVMOrcExecutorAddress(s.Value.Address)
		cpairs[i].Sym.Flags.GenericFlags = C.uint8_t(s.Value.Flags.Generic)
		cpairs[i].Sym.Flags.TargetFlags = C.uint8_t(s.Value.Flags.Target)
	}
	mu.C = C.LLVMOrcAbsoluteSymbols(&cpairs[0], C.size_t(len(cpairs)))
	return
}

//-------------------------------------------------------------------------
// llvm.JITDylib / llvm.DefinitionGenerator
//-------------------------------------------------------------------------

// JITDylib is a symbol table that JIT'd code and definition generators
// populate and LLJIT resolves lookups/calls against. Every LLJIT instance
// has at least one - its "main" JITDylib (see LLJIT.MainJITDylib).
type JITDylib struct {
	C C.LLVMOrcJITDylibRef
}

// AddGenerator attaches dg to jd, taking ownership of it: dg is consulted
// whenever a lookup against jd fails to match an existing definition (see
// NewDynamicLibrarySearchGeneratorForProcess for the common case of this -
// making the host process's own symbols, e.g. libc functions, resolvable
// from JIT'd code).
func (jd JITDylib) AddGenerator(dg DefinitionGenerator) {
	C.LLVMOrcJITDylibAddGenerator(jd.C, dg.C)
}

// CreateResourceTracker returns a newly created ResourceTracker associated
// with jd - see ResourceTracker's own doc comment for what it's for. The
// caller must call ResourceTracker.Release once it's no longer needed.
func (jd JITDylib) CreateResourceTracker() (rt ResourceTracker) {
	rt.C = C.LLVMOrcJITDylibCreateResourceTracker(jd.C)
	return
}

// DefaultResourceTracker returns jd's own default resource tracker - the one
// that implicitly tracks anything added to jd without an explicit
// ResourceTracker of its own (e.g. via LLJIT.AddLLVMIRModule rather than
// AddLLVMIRModuleWithRT). The caller must call ResourceTracker.Release once
// it's no longer needed.
func (jd JITDylib) DefaultResourceTracker() (rt ResourceTracker) {
	rt.C = C.LLVMOrcJITDylibGetDefaultResourceTracker(jd.C)
	return
}

// Define adds mu to jd, taking ownership of it on success: mu must not be
// used or disposed of afterward. On failure, ownership remains with the
// caller, who should call MaterializationUnit.Dispose to destroy it (mu is
// still safe to Dispose in that case only - not after a successful Define).
func (jd JITDylib) Define(mu MaterializationUnit) error {
	return orcError(C.LLVMOrcJITDylibDefine(jd.C, mu.C))
}

// ObjectLayer is the layer an LLJIT instance ultimately links JIT'd object
// code through (see LLJIT.ObjLinkingLayer) - this package only exposes it as
// an opaque handle, for passing to NewStaticLibrarySearchGeneratorForPath.
type ObjectLayer struct {
	C C.LLVMOrcObjectLayerRef
}

// DefinitionGenerator is attached to a JITDylib (via JITDylib.AddGenerator)
// to supply definitions for symbols it doesn't already have - most commonly
// used to reflect an external set of symbols (e.g. the host process's own,
// see NewDynamicLibrarySearchGeneratorForProcess) into a JITDylib.
type DefinitionGenerator struct {
	C C.LLVMOrcDefinitionGeneratorRef
}

// NewDynamicLibrarySearchGeneratorForProcess creates a DefinitionGenerator
// that reflects the host process's own symbols (i.e. anything already
// loaded into this process, whether from a shared library or the main
// executable itself) into whichever JITDylib it's attached to (see
// JITDylib.AddGenerator) - this is what lets JIT'd code call ordinary libc
// functions like printf without the caller manually mapping every one of
// them by hand (see ExecutionEngine.AddGlobalMapping for how the older MCJIT
// API required doing this).
//
// globalPrefix is the character that appears on the front of linker-mangled
// symbols for the target platform (e.g. '_' on Mach-O) - if non-zero, it's
// stripped from a symbol name before it's looked up in the process; pass 0
// for platforms with no such prefix.
func NewDynamicLibrarySearchGeneratorForProcess(globalPrefix byte) (dg DefinitionGenerator, err error) {
	cerr := C.LLVMOrcCreateDynamicLibrarySearchGeneratorForProcess(&dg.C, C.char(globalPrefix), nil, nil)
	err = orcError(cerr)
	return
}

// NewDynamicLibrarySearchGeneratorForPath creates a DefinitionGenerator that
// loads path (a shared library) and reflects its symbols into whichever
// JITDylib it's attached to (see JITDylib.AddGenerator) - the same idea as
// NewDynamicLibrarySearchGeneratorForProcess, but for an arbitrary shared
// library rather than the symbols already loaded into the host process.
//
// globalPrefix is the same per-platform linker-mangling prefix character
// NewDynamicLibrarySearchGeneratorForProcess takes; pass 0 if the target
// platform has none.
//
// This is marked experimental by LLVM itself and may change in a future
// LLVM release.
func NewDynamicLibrarySearchGeneratorForPath(path string, globalPrefix byte) (dg DefinitionGenerator, err error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cerr := C.LLVMOrcCreateDynamicLibrarySearchGeneratorForPath(&dg.C, cpath, C.char(globalPrefix), nil, nil)
	err = orcError(cerr)
	return
}

// NewStaticLibrarySearchGeneratorForPath creates a DefinitionGenerator that
// reflects the symbols of the static library (or MachO universal binary
// containing one) at path into whichever JITDylib it's attached to (see
// JITDylib.AddGenerator). objLayer is the object layer the generator will
// use to load matched archive members - see LLJIT.ObjLinkingLayer.
//
// This is marked experimental by LLVM itself and may change in a future
// LLVM release.
func NewStaticLibrarySearchGeneratorForPath(objLayer ObjectLayer, path string) (dg DefinitionGenerator, err error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cerr := C.LLVMOrcCreateStaticLibrarySearchGeneratorForPath(&dg.C, objLayer.C, cpath)
	err = orcError(cerr)
	return
}

//-------------------------------------------------------------------------
// llvm.ResourceTracker
//-------------------------------------------------------------------------

// ResourceTracker tracks a set of resources (JIT'd code, and everything
// associated with it) added to a JITDylib, so that set can be removed as a
// unit - unlike a single MCJIT ExecutionEngine, where the only way to free
// anything was disposing of the whole engine, ResourceTracker lets a caller
// unload just the modules it no longer needs from a still-running LLJIT
// instance. See JITDylib.CreateResourceTracker/DefaultResourceTracker, and
// LLJIT.AddLLVMIRModuleWithRT/AddObjectFileWithRT for attaching new
// resources to a specific tracker instead of a JITDylib's default one.
type ResourceTracker struct {
	C C.LLVMOrcResourceTrackerRef
}

// Release releases this reference to rt - every ResourceTracker obtained
// from this package (via CreateResourceTracker or DefaultResourceTracker)
// must eventually have Release called on it, whether or not Remove is ever
// called too.
func (rt ResourceTracker) Release() {
	C.LLVMOrcReleaseResourceTracker(rt.C)
}

// TransferTo moves tracking of every resource rt currently tracks over to
// dst, leaving rt tracking nothing.
func (rt ResourceTracker) TransferTo(dst ResourceTracker) {
	C.LLVMOrcResourceTrackerTransferTo(rt.C, dst.C)
}

// Remove removes every resource rt tracks - any JIT'd code it covers is
// unloaded, and any symbols it defined are removed from their JITDylib.
func (rt ResourceTracker) Remove() error {
	return orcError(C.LLVMOrcResourceTrackerRemove(rt.C))
}

//-------------------------------------------------------------------------
// llvm.LLJITBuilder / llvm.LLJIT
//-------------------------------------------------------------------------

// LLJITBuilder configures and creates an LLJIT instance (see NewLLJIT).
type LLJITBuilder struct {
	C C.LLVMOrcLLJITBuilderRef
}

// NewLLJITBuilder creates a default-configured LLJITBuilder - passing it
// straight to NewLLJIT builds an LLJIT targeting the host, equivalent to
// calling SetJITTargetMachineBuilder with
// NewJITTargetMachineBuilderDetectHost's result explicitly.
func NewLLJITBuilder() (b LLJITBuilder) {
	b.C = C.LLVMOrcCreateLLJITBuilder()
	return
}

// SetJITTargetMachineBuilder sets the target b's resulting LLJIT instance
// will generate code for, taking ownership of jtmb: it must not be used or
// disposed of afterward. Calling this is optional - see NewLLJITBuilder.
func (b LLJITBuilder) SetJITTargetMachineBuilder(jtmb JITTargetMachineBuilder) {
	C.LLVMOrcLLJITBuilderSetJITTargetMachineBuilder(b.C, jtmb.C)
}

// Dispose releases b. This must only be called if b was never passed to
// NewLLJIT, or NewLLJIT itself returned an error - a successful NewLLJIT
// call takes ownership of b instead.
func (b LLJITBuilder) Dispose() {
	C.LLVMOrcDisposeLLJITBuilder(b.C)
}

// LLJIT is an ORCv2-based JIT compiler and execution engine - the modern
// replacement for the legacy MCJIT-based ExecutionEngine (executionengine.go)
// for new code. Unlike ExecutionEngine, an LLJIT instance's memory
// automatically covers every module transferred to it (see
// AddLLVMIRModule) and everything the JIT itself allocates for them - a
// single Dispose call tears all of it down together, in the correct order.
type LLJIT struct {
	C C.LLVMOrcLLJITRef
}

// NewLLJIT creates an LLJIT instance from builder, taking ownership of it:
// builder must not be used or disposed of afterward, whether or not this
// call succeeds.
func NewLLJIT(builder LLJITBuilder) (jit LLJIT, err error) {
	cerr := C.LLVMOrcCreateLLJIT(&jit.C, builder.C)
	err = orcError(cerr)
	return
}

// Dispose tears down jit, freeing every module transferred to it (see
// AddLLVMIRModule) along with all memory the JIT itself allocated for them
// (JIT'd code included) - jit must not be used again afterward.
func (jit LLJIT) Dispose() error {
	return orcError(C.LLVMOrcDisposeLLJIT(jit.C))
}

// MainJITDylib returns jit's default JITDylib - the one AddLLVMIRModule
// resolves definitions into and against unless a caller sets up additional
// JITDylibs of its own (not yet bound by this package). Its memory is owned
// by jit; it needs no Dispose of its own.
func (jit LLJIT) MainJITDylib() (jd JITDylib) {
	jd.C = C.LLVMOrcLLJITGetMainJITDylib(jit.C)
	return
}

// AddLLVMIRModule adds tsm to jd, taking ownership of it: tsm must not be
// used, disposed of, or added anywhere else again afterward, whether or not
// this call succeeds. Every symbol tsm's module defines becomes resolvable
// via Lookup once this returns, and becomes callable from (and able to call)
// any other module already added to jit - unlike a single MCJIT
// ExecutionEngine bound to one module, an LLJIT instance's JITDylib is a
// shared symbol table across every module added to it.
func (jit LLJIT) AddLLVMIRModule(jd JITDylib, tsm ThreadSafeModule) error {
	return orcError(C.LLVMOrcLLJITAddLLVMIRModule(jit.C, jd.C, tsm.C))
}

// AddLLVMIRModuleWithRT is AddLLVMIRModule, but tracks tsm's resources under
// rt (see ResourceTracker) instead of rt's JITDylib's default tracker - use
// this over AddLLVMIRModule when the caller wants to be able to unload tsm
// later (via ResourceTracker.Remove) independently of anything else added to
// the same JITDylib.
func (jit LLJIT) AddLLVMIRModuleWithRT(rt ResourceTracker, tsm ThreadSafeModule) error {
	return orcError(C.LLVMOrcLLJITAddLLVMIRModuleWithRT(jit.C, rt.C, tsm.C))
}

// AddObjectFile adds a precompiled object file (buf - e.g. loaded via
// NewMemoryBufferFromFile) to jd, taking ownership of buf: it must not be
// used, disposed of, or added anywhere else again afterward, whether or not
// this call succeeds. Every symbol the object file defines becomes
// resolvable via Lookup once this returns, the same as a module added via
// AddLLVMIRModule. Resources associated with buf are tracked by jd's
// default resource tracker - see AddObjectFileWithRT to track them
// separately instead.
func (jit LLJIT) AddObjectFile(jd JITDylib, buf MemoryBuffer) error {
	return orcError(C.LLVMOrcLLJITAddObjectFile(jit.C, jd.C, buf.C))
}

// AddObjectFileWithRT is AddObjectFile, but tracks buf's resources under rt
// (see ResourceTracker) instead of rt's JITDylib's default tracker.
func (jit LLJIT) AddObjectFileWithRT(rt ResourceTracker, buf MemoryBuffer) error {
	return orcError(C.LLVMOrcLLJITAddObjectFileWithRT(jit.C, rt.C, buf.C))
}

// Lookup resolves name (a plain, unmangled symbol name - e.g. a function
// name exactly as declared in the source IR) against jit's JITDylibs,
// JIT-compiling whatever hasn't been compiled yet along the way, and
// returns its address in the executor process. This is the ORC counterpart
// of ExecutionEngine.GetFunctionAddress: the caller is responsible for
// actually invoking the returned address (e.g. via a cgo trampoline, or a
// platform-specific raw-call mechanism) - LLJIT's own C API stops at
// resolving the address, same as MCJIT's did.
func (jit LLJIT) Lookup(name string) (addr uint64, err error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var result C.LLVMOrcExecutorAddress
	cerr := C.LLVMOrcLLJITLookup(jit.C, &result, cname)
	if err = orcError(cerr); err != nil {
		return 0, err
	}
	return uint64(result), nil
}

// TripleString returns the target triple jit was configured for.
func (jit LLJIT) TripleString() string {
	return C.GoString(C.LLVMOrcLLJITGetTripleString(jit.C))
}

// GlobalPrefix returns the character that appears on the front of
// linker-mangled symbols for jit's target platform (e.g. '_' on Mach-O, 0
// for platforms with no such prefix) - the same value
// NewDynamicLibrarySearchGeneratorForProcess expects for its own
// globalPrefix argument.
func (jit LLJIT) GlobalPrefix() byte {
	return byte(C.LLVMOrcLLJITGetGlobalPrefix(jit.C))
}

// DataLayoutStr returns jit's default data layout string.
func (jit LLJIT) DataLayoutStr() string {
	return C.GoString(C.LLVMOrcLLJITGetDataLayoutStr(jit.C))
}

// ExecutionSession returns a reference to jit's own ExecutionSession - the
// entry point for creating additional JITDylibs beyond jit.MainJITDylib
// (see ExecutionSession.CreateJITDylib). Its memory is owned by jit; it
// needs no Dispose of its own.
func (jit LLJIT) ExecutionSession() (es ExecutionSession) {
	es.C = C.LLVMOrcLLJITGetExecutionSession(jit.C)
	return
}

// ObjLinkingLayer returns a non-owning reference to jit's object linking
// layer - the layer AddObjectFile/AddLLVMIRModule ultimately link JIT'd code
// through. Its main use in this package is as the objLayer argument to
// NewStaticLibrarySearchGeneratorForPath.
func (jit LLJIT) ObjLinkingLayer() (ol ObjectLayer) {
	ol.C = C.LLVMOrcLLJITGetObjLinkingLayer(jit.C)
	return
}

// MangleAndIntern mangles name according to jit's own DataLayout (see
// DataLayoutStr) and interns the result in its ExecutionSession's
// SymbolStringPool, returning a reference to it - the caller must call
// SymbolStringPoolEntry.Release once it's done with the result. Unlike
// ExecutionSession.Intern, this is the right call for a plain, unmangled
// source-level name (e.g. one about to be used with AbsoluteSymbols to
// expose a host function under that exact source name).
func (jit LLJIT) MangleAndIntern(name string) (entry SymbolStringPoolEntry) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	entry.C = C.LLVMOrcLLJITMangleAndIntern(jit.C, cname)
	return
}

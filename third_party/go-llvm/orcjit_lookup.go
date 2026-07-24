//go:build llvm22

//===- orcjit_lookup.go - ExecutionSession.Lookup for orcjit --------------===//
//
// Part of the LLVM Project, under the Apache License v2.0 with LLVM Exceptions.
// See https://llvm.org/LICENSE.txt for license information.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
//===----------------------------------------------------------------------===//
//
// LLVMOrcExecutionSessionLookup - the general lookup that can search a
// caller-chosen set of JITDylibs, unlike LLJIT.Lookup (main JITDylib only,
// see orcjit.go) - reports its result through a callback rather than a
// return value, so this needs real cgo callback machinery: a //export'd Go
// function ORC can call from wherever LLVMOrcExecutionSessionLookup's own
// completion path runs (possibly, though not necessarily, a different
// thread than the caller's), and a way to get that result back to the
// specific goroutine waiting on it.
//
// This lives in its own file, separate from orcjit.go, because a file using
// //export is restricted to a preamble containing only declarations, never
// definitions (the preamble is copied into two different generated C files,
// so a definition in it would produce a duplicate symbol at link time) -
// orcjit_call.go's static inline helper functions make orcjit.go's own
// preamble ineligible for that restriction.
//
// This package's go.mod floor (go 1.14) predates both runtime/cgo.Handle and
// unsafe.Slice (both go1.17), which would otherwise be the obvious tools for
// this - a Handle to pass the waiting goroutine's channel through the
// callback's void* context without violating cgo's Go-pointer-passing
// rules, and unsafe.Slice to view the returned C array as a Go slice.
// Instead this uses their pre-1.17 equivalents: a manual token -> channel
// registry (see esLookupToken/goOrcExecutionSessionLookupResult) and the
// classic bounded-fake-array-then-reslice idiom for the C array.
//
//===----------------------------------------------------------------------===//

package llvm

/*
#include "llvm-c/Orc.h"
#include "llvm-c/LLJIT.h"
#include "llvm-c/Error.h"

// Forward-declares the //export'd Go function below so C.
// goOrcExecutionSessionLookupResult resolves within this same file - cgo
// generates the real definition (in _cgo_export.h/.c) from the //export
// comment itself, this is only a declaration, so it doesn't run afoul of
// the declarations-only preamble restriction //export otherwise imposes.
extern void goOrcExecutionSessionLookupResult(LLVMErrorRef Err,
                                               LLVMOrcCSymbolMapPairs Result,
                                               size_t NumPairs, void *Ctx);
*/
import "C"
import (
	"runtime"
	"sync"
	"unsafe"
)

// esLookupResult is what goOrcExecutionSessionLookupResult hands back to
// the goroutine waiting in ExecutionSession.Lookup.
type esLookupResult struct {
	addr uint64
	err  error
}

var (
	esLookupMu      sync.Mutex
	esLookupResults = map[uintptr]chan esLookupResult{}
)

// esLookupToken is a dedicated heap allocation whose own address (never
// otherwise dereferenced) serves as ExecutionSession.Lookup's opaque
// identity for a single in-flight call, registered/looked-up by
// esLookupResults keyed on uintptr(unsafe.Pointer(token)).
//
// This exists purely to keep every unsafe.Pointer/uintptr conversion here
// running in the one direction `go vet`'s unsafeptr check actually allows -
// pointer to uintptr (safe/unflagged), never uintptr to pointer (flagged,
// even for a bare integer id that's never treated as a real pointer on
// either side of the C call - which is otherwise a perfectly standard
// pre-runtime/cgo.Handle idiom). Go's own garbage collector has no way to
// know C is holding this address for the duration of the call below, so
// ExecutionSession.Lookup calls runtime.KeepAlive(token) right up until it
// no longer needs to be reachable.
type esLookupToken struct{}

// goOrcExecutionSessionLookupResult is LLVMOrcExecutionSessionLookup's
// HandleResult callback - see ExecutionSession.Lookup, the only place that
// ever registers a channel for it to find via cctx.
//
//export goOrcExecutionSessionLookupResult
func goOrcExecutionSessionLookupResult(cerr C.LLVMErrorRef, cresult C.LLVMOrcCSymbolMapPairs, numPairs C.size_t, cctx unsafe.Pointer) {
	id := uintptr(cctx)

	esLookupMu.Lock()
	ch := esLookupResults[id]
	delete(esLookupResults, id)
	esLookupMu.Unlock()

	if err := orcError(cerr); err != nil {
		ch <- esLookupResult{err: err}
		return
	}

	// cresult is a real C array of numPairs LLVMOrcCSymbolMapPair elements -
	// see this file's own doc comment for why this doesn't just use
	// unsafe.Slice.
	const maxLookupResults = 1 << 20
	pairs := (*[maxLookupResults]C.LLVMOrcCSymbolMapPair)(unsafe.Pointer(cresult))[:int(numPairs):int(numPairs)]
	ch <- esLookupResult{addr: uint64(pairs[0].Sym.Address)}
}

// Lookup searches searchOrder (in order, exported symbols only) for name,
// JIT-compiling whatever hasn't been compiled yet along the way, and
// returns its address - the general form of LLJIT.Lookup, which only ever
// searches the main JITDylib. name is treated as required: a failed lookup
// is a real error, never a zero result.
//
// name is interned internally (see ExecutionSession.Intern) - pass a plain,
// unmangled source-level name, the same as LLJIT.Lookup.
func (es ExecutionSession) Lookup(searchOrder []JITDylib, name string) (addr uint64, err error) {
	sym := es.Intern(name)
	defer sym.Release()

	// A JITDylib search order list is null(JD)-terminated - see Orc.h's own
	// doc comment for LLVMOrcCJITDylibSearchOrder - in addition to the
	// explicit SearchOrderSize LLVMOrcExecutionSessionLookup also takes;
	// corder's last element is left zero-valued to provide that terminator.
	corder := make([]C.LLVMOrcCJITDylibSearchOrderElement, len(searchOrder)+1)
	for i, jd := range searchOrder {
		corder[i].JD = jd.C
		corder[i].JDLookupFlags = C.LLVMOrcJITDylibLookupFlagsMatchExportedSymbolsOnly
	}

	// Likewise a symbol lookup set is null(Name)-terminated; cset[1] is left
	// zero-valued for that.
	cset := make([]C.LLVMOrcCLookupSetElement, 2)
	cset[0].Name = sym.C
	cset[0].LookupFlags = C.LLVMOrcSymbolLookupFlagsRequiredSymbol

	ch := make(chan esLookupResult, 1)
	token := new(esLookupToken)
	id := uintptr(unsafe.Pointer(token))

	esLookupMu.Lock()
	esLookupResults[id] = ch
	esLookupMu.Unlock()

	C.LLVMOrcExecutionSessionLookup(
		es.C,
		C.LLVMOrcLookupKindStatic,
		&corder[0], C.size_t(len(searchOrder)),
		&cset[0], C.size_t(1),
		C.LLVMOrcExecutionSessionLookupHandleResultFunction(C.goOrcExecutionSessionLookupResult),
		unsafe.Pointer(token),
	)

	result := <-ch
	// token must stay reachable for as long as C might still call back with
	// its address - see esLookupToken's own doc comment.
	runtime.KeepAlive(token)
	return result.addr, result.err
}

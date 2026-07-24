//go:build llvm22

//===- orcjit_call.go - Test support for orcjit ---------------------------===//
//
// Part of the LLVM Project, under the Apache License v2.0 with LLVM Exceptions.
// See https://llvm.org/LICENSE.txt for license information.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
//===----------------------------------------------------------------------===//
//
// LLJIT.Lookup only hands back a resolved address (see its own doc comment
// in orcjit.go) - actually invoking it is deliberately out of scope for the
// C API itself, same as it was for ExecutionEngine.GetFunctionAddress, so a
// real caller is expected to bring its own call mechanism (a cgo shim like
// this one, a platform-specific raw call, and so on).
//
// This exists as its own non-test file, rather than living directly in
// orcjit_test.go, because `go test` rejects `import "C"` inside a _test.go
// file outright ("use of cgo in test ... not supported") - both of these
// helpers are unexported and used only by orcjit_test.go, so this carries no
// public API surface of its own.
//
//===----------------------------------------------------------------------===//

package llvm

/*
#include <stdint.h>
#include <stdlib.h>

typedef int32_t (*i32_fn_i32)(int32_t);
typedef int32_t (*i32_fn_void)(void);

static int32_t llvm_go_call_i32_fn_i32(uint64_t addr, int32_t arg) {
	i32_fn_i32 fn = (i32_fn_i32)(uintptr_t)addr;
	return fn(arg);
}

static int32_t llvm_go_call_i32_fn_void(uint64_t addr) {
	i32_fn_void fn = (i32_fn_void)(uintptr_t)addr;
	return fn();
}

// Address of the host's own `abs`, for TestLLJITAbsoluteSymbols - a real
// raw address AbsoluteSymbols can bind a JIT-visible name to directly,
// independent of NewDynamicLibrarySearchGeneratorForProcess's own
// dlsym-style resolution (already exercised by
// TestLLJITProcessSymbolGenerator).
static uint64_t llvm_go_abs_address(void) {
	return (uint64_t)(uintptr_t)&abs;
}
*/
import "C"

// callI32FnI32 calls the JIT'd function at addr - of C signature
// `int32_t(int32_t)` - with arg, returning its result.
func callI32FnI32(addr uint64, arg int32) int32 {
	return int32(C.llvm_go_call_i32_fn_i32(C.uint64_t(addr), C.int32_t(arg)))
}

// callI32FnVoid calls the JIT'd function at addr - of C signature
// `int32_t(void)` - returning its result.
func callI32FnVoid(addr uint64) int32 {
	return int32(C.llvm_go_call_i32_fn_void(C.uint64_t(addr)))
}

// absAddress returns the host process's own `abs` function's address.
func absAddress() uint64 {
	return uint64(C.llvm_go_abs_address())
}

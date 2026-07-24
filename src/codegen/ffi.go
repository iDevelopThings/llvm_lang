package codegen

import (
	"fmt"

	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// This file is struct-by-value FFI's own ABI-coercion logic - the piece
// beyond "just declare/call using g.llvmType" that sema's own allowlist
// (isFFISafeType, sema/typecheck.go) assumes exists (see LANGUAGE.md's
// "External functions (FFI)" section): the Windows x64 calling convention
// doesn't let a real aggregate cross a call boundary as-is the way LLVM's
// own default IR lowering would flatten it (each field becomes its own,
// independently-assigned register/stack slot) - verified empirically to
// silently corrupt a 2-field struct's second field, not just theorized
// (see DECISIONS.md's dated entry). A struct of size 1/2/4/8 bytes must
// instead be coerced to a same-size integer; anything else crosses by
// reference instead. Scoped to extern func declare/call only (genFuncCall) -
// an intra-language call never diverges, since caller and callee are both
// lowered by the same backend applying the same (ABI-incorrect, but
// internally consistent) default flattening on both sides alike.

// abiSizeAlign computes t's real C-ABI byte size/alignment - the
// classification externParamType/externReturnType need (Windows x64: a
// struct/union of size 1, 2, 4, or 8 bytes is passed/returned as an integer
// of that size; anything else goes by reference). Computed directly from
// field types rather than via LLVM's own TargetData, since codegen runs
// before the module's DataLayout is pinned (see compiler.finishPipeline) -
// mirrors the natural-alignment layout StructSetBody(fieldTypes, false)
// will later produce under that same DataLayout, for this project's one
// supported target (x86-64 - see DECISIONS.md). Only ever called on a type
// sema's isFFISafeType/isFFISafeStructField already accepted, so every
// TypeKind reachable here has a well-defined size.
func (g *Generator) abiSizeAlign(t sema.Type) (size, align uint64) {
	switch t.Kind {
	case sema.TypeI8, sema.TypeBool:
		return 1, 1
	case sema.TypeI16:
		return 2, 2
	case sema.TypeI32, sema.TypeF32:
		return 4, 4
	case sema.TypeI64, sema.TypeF64, sema.TypePointer, sema.TypeCString, sema.TypeCFunc:
		// cfunc lowers to a bare function pointer - same size/align as ptr.
		return 8, 8
	case sema.TypeArray:
		elemSize, elemAlign := g.abiSizeAlign(*t.Elem)
		return roundUpToAlign(elemSize, elemAlign) * uint64(t.Size), elemAlign
	case sema.TypeStruct:
		var size, align uint64 = 0, 1
		for _, ft := range g.structLayouts[t.Struct].fieldSemaTypes {
			fSize, fAlign := g.abiSizeAlign(ft)
			align = max(align, fAlign)
			size = roundUpToAlign(size, fAlign) + fSize
		}
		return roundUpToAlign(size, align), align
	default:
		panic(fmt.Sprintf("codegen: abiSizeAlign called on non-FFI-safe type %s", t))
	}
}

func roundUpToAlign(n, align uint64) uint64 {
	return (n + align - 1) / align * align
}

// coercedIntType returns the LLVM integer type the Windows x64 ABI coerces
// a size-byte aggregate to (i8/i16/i32/i64), or ok=false when size doesn't
// qualify (anything but exactly 1, 2, 4, or 8 bytes crosses by reference
// instead - see abiSizeAlign's own doc comment).
func (g *Generator) coercedIntType(size uint64) (t llvm.Type, ok bool) {
	switch size {
	case 1:
		return g.i8Ty, true
	case 2:
		return g.i16Ty, true
	case 4:
		return g.i32Ty, true
	case 8:
		return g.i64Ty, true
	default:
		return llvm.Type{}, false
	}
}

// externParamType returns the real LLVM type an extern func's own parameter
// of sema type t must be declared with - g.llvmType(t) unchanged for
// anything but a struct, which gets the Windows x64 aggregate-ABI
// classification (abiSizeAlign/coercedIntType) instead: a same-size integer
// when it fits, otherwise g.ptrTy (the caller allocates a temp copy and
// passes its address - see genFuncCall's coerceExternArg).
func (g *Generator) externParamType(t sema.Type) llvm.Type {
	if t.Kind != sema.TypeStruct {
		return g.llvmType(t)
	}
	size, _ := g.abiSizeAlign(t)
	if intTy, ok := g.coercedIntType(size); ok {
		return intTy
	}
	return g.ptrTy
}

// externReturnType is externParamType's return-position counterpart. A
// struct return that doesn't fit the "as an integer" case becomes a void
// LLVM return with a hidden leading sret pointer parameter instead (real
// Windows x64 ABI: the caller allocates the return slot and passes its
// address as an implicit first argument, shifting every declared parameter
// right by one - see LANGUAGE.md/CODEGEN.md's "External functions (FFI)"
// sections) - the second result reports whether that hidden parameter was
// added, so declareExternFuncSignature/genFuncCall both know to thread it
// through.
func (g *Generator) externReturnType(t sema.Type) (retType llvm.Type, sretReturn bool) {
	if t.Kind != sema.TypeStruct {
		return g.llvmType(t), false
	}
	size, _ := g.abiSizeAlign(t)
	if intTy, ok := g.coercedIntType(size); ok {
		return intTy, false
	}
	return g.voidTy, true
}

// bitcastThroughMemory reinterprets val's bit pattern as toType via a fresh
// temp alloca - LLVM has no direct struct<->integer bitcast on an SSA value,
// so this is the standard way to cross that boundary (used by both
// directions of coerceExternArg's caller, genFuncCall: struct-to-int for an
// argument, int-to-struct for a coerced return value).
func (g *Generator) bitcastThroughMemory(val llvm.Value, toType llvm.Type) llvm.Value {
	slot := g.builder.CreateAlloca(val.Type(), "")
	g.builder.CreateStore(val, slot)
	return g.builder.CreateLoad(toType, slot, "")
}

// coerceExternArg adapts val (already in its natural LLVM representation,
// e.g. a real struct-typed SSA value) to declared - the real LLVM type
// externParamType chose for this position. A no-op whenever the two already
// match (every non-struct argument, and every intra-language call, which
// never diverges - see this file's own doc comment). A struct that the
// Windows ABI coerces to an integer is reinterpreted via bitcastThroughMemory;
// one passed by reference instead gets a fresh stack copy, passing that
// copy's address directly (declared is g.ptrTy in that case).
func (g *Generator) coerceExternArg(val llvm.Value, declared llvm.Type) llvm.Value {
	if val.Type() == declared {
		return val
	}
	if declared.TypeKind() == llvm.PointerTypeKind {
		slot := g.builder.CreateAlloca(val.Type(), "")
		g.builder.CreateStore(val, slot)
		return slot
	}
	return g.bitcastThroughMemory(val, declared)
}

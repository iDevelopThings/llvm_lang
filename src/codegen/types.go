package codegen

import (
	"fmt"

	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// setupTypes computes every primitive LLVM type this package needs, once per
// Generator. See AGENTS.md's codegen section for the decisions baked in
// here:
//   - `int` is i32, not i64 - it matches C's (and so LLVM's real entry
//     point's) `int` exactly, so `main`'s exit code never needs a cast, and
//     every ConstInt/GenericValue in this package and its tests uses one
//     consistent width. i8/i16/i64 are real, distinct additional widths;
//     f32/f64 map directly to LLVM's own float/double types.
//   - `string` is the literal (unnamed) struct {ptr, i32} - a data pointer
//     plus a length, not a null-terminated C string. It's never guaranteed
//     null-terminated, so every consumer (print, concatenation, equality)
//     goes through the length field, never strlen.
//   - a first-class function value (sema.TypeFunc) is the literal (unnamed)
//     struct {ptr, ptr} - a "fat pointer" of {fnPtr, ctxPtr} - see
//     CODEGEN.md's "First-class functions" section and genFuncValue
//     (expr.go) for the one construction site that fills it in.
func (g *Generator) setupTypes() {
	g.i8Ty = g.ctx.Int8Type()
	g.i16Ty = g.ctx.Int16Type()
	g.i32Ty = g.ctx.Int32Type()
	g.i64Ty = g.ctx.Int64Type()
	g.f32Ty = g.ctx.FloatType()
	g.f64Ty = g.ctx.DoubleType()
	g.boolTy = g.ctx.Int1Type()
	g.ptrTy = llvm.PointerType(g.i8Ty, 0)
	g.voidTy = g.ctx.VoidType()
	g.stringTy = g.ctx.StructType([]llvm.Type{g.ptrTy, g.i32Ty}, false)
	g.funcValTy = g.ctx.StructType([]llvm.Type{g.ptrTy, g.ptrTy}, false)
}

// llvmType maps a sema.Type (the output of type-checking) to the LLVM type
// codegen represents it with. Every case here assumes t was produced by
// type-checking a valid program - TypeInvalid, and either untyped-constant
// kind, never legitimately reach this (see the package doc comment: sema
// always resolves an untyped constant to a concrete type before it lands in
// info.Types anywhere codegen reads it - see AGENTS.md's Types section),
// hence the panic in default rather than a returned error.
func (g *Generator) llvmType(t sema.Type) llvm.Type {
	switch t.Kind {
	case sema.TypeI8:
		return g.i8Ty
	case sema.TypeI16:
		return g.i16Ty
	case sema.TypeI32: // also TypeInt - the same constant, see sema/types.go
		return g.i32Ty
	case sema.TypeI64:
		return g.i64Ty
	case sema.TypeF32:
		return g.f32Ty
	case sema.TypeF64:
		return g.f64Ty
	case sema.TypeBool:
		return g.boolTy
	case sema.TypeString:
		return g.stringTy
	case sema.TypeVoid:
		return g.voidTy
	case sema.TypeStruct:
		return g.structLayouts[t.Struct].llvmType
	case sema.TypeArray:
		return llvm.ArrayType(g.llvmType(*t.Elem), int(t.Size))
	case sema.TypeFunc:
		return g.funcValTy
	default:
		panic(fmt.Sprintf("codegen: type %s reached llvmType - only valid, checked types are supported", t))
	}
}

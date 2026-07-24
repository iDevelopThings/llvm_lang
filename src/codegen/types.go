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
//     (expr.go) for the one construction site that fills it in. A bare C
//     function pointer (sema.TypeCFunc) is just g.ptrTy alone - no ctxPtr,
//     no fat pointer at all - see CODEGEN.md's "External functions (FFI)"
//     section.
//   - a dynamic array (sema.Type{Kind: TypeArray, Dynamic: true}) is the
//     literal (unnamed) struct {ptr, i32, i32} - a data pointer, current
//     length, and allocated capacity - following the exact same convention
//     `string`'s {ptr, i32} already uses, just with a third field. One
//     shared LLVM struct type serves every element type: g.ptrTy is already
//     an opaque `ptr` (see its own field comment) regardless of what it
//     points to, so the element type only ever matters at the point a GEP
//     computes an address into the backing buffer (genMakeCall/genAppendCall/
//     genAddr's IndexExpr case, expr.go), never in the struct's own shape.
//     See CODEGEN.md's "Dynamic arrays" section.
//   - an enum value (sema.Type{Kind: TypeEnum}) is the literal (unnamed)
//     struct {i32, ptr} - a discriminant (the active variant's own
//     declaration-order index) plus an opaque payload pointer, arena-
//     allocated the moment a non-unit variant is actually constructed (null
//     for a unit variant, which carries no associated data at all) - see
//     CODEGEN.md's "Enums" section and enum.go. One shared LLVM struct type
//     serves every enum type, exactly like dynArrTy/funcValTy above: the
//     payload is always just an opaque address regardless of which enum (or
//     which of its variants) is active, so there's nothing enum-specific
//     for the outer struct's own shape to encode - only the *payload's own*
//     type (built per-variant in enum.go's declareEnumLayouts) ever depends
//     on a specific enum's own declared variants.
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
	g.dynArrTy = g.ctx.StructType([]llvm.Type{g.ptrTy, g.i32Ty, g.i32Ty}, false)
	g.enumValTy = g.ctx.StructType([]llvm.Type{g.i32Ty, g.ptrTy}, false)
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
	// Unsigned widths share their signed counterpart's LLVM type - LLVM ints
	// carry no signedness; the sema Type drives instruction selection instead.
	case sema.TypeU8:
		return g.i8Ty
	case sema.TypeU16:
		return g.i16Ty
	case sema.TypeU32:
		return g.i32Ty
	case sema.TypeU64:
		return g.i64Ty
	case sema.TypeF32:
		return g.f32Ty
	case sema.TypeF64:
		return g.f64Ty
	case sema.TypeBool:
		return g.boolTy
	case sema.TypeString:
		return g.stringTy
	case sema.TypeCString:
		// A raw C string is just a pointer at the ABI level - no length
		// field, unlike TypeString's {ptr, i32} (see LANGUAGE.md's
		// "External functions (FFI)" section).
		return g.ptrTy
	case sema.TypeVoid:
		return g.voidTy
	case sema.TypeStruct:
		return g.structLayouts[t.Struct].llvmType
	case sema.TypeEnum:
		return g.enumValTy
	case sema.TypeArray:
		if t.Dynamic {
			return g.dynArrTy
		}
		return llvm.ArrayType(g.llvmType(*t.Elem), int(t.Size))
	case sema.TypeFunc:
		return g.funcValTy
	case sema.TypeCFunc:
		// A bare C function pointer (see LANGUAGE.md's "External functions
		// (FFI)" section) - a single opaque `ptr`, unlike TypeFunc's own
		// {fnPtr, ctxPtr} fat pointer above: there is no capture context at
		// all, so g.ptrTy alone is sema.TypeCFunc's exact real representation.
		return g.ptrTy
	case sema.TypeMultiReturn:
		// A multi-return function's real LLVM signature returns an anonymous
		// struct {T1, T2, ...} - exactly the same "return a struct by value"
		// machinery an ordinary struct-returning function already uses (see
		// CODEGEN.md's "Go-style multi-return values" section), just built
		// from this Type's own component Params rather than a StructInfo's
		// declared fields. No dedicated LLVM type is cached for this the way
		// stringTy/dynArrTy/funcValTy are - unlike those three (one fixed
		// shape reused everywhere), every multi-return function's own
		// component types differ, so there's nothing to precompute once in
		// setupTypes.
		fieldTypes := make([]llvm.Type, len(t.Params))
		for i, pt := range t.Params {
			fieldTypes[i] = g.llvmType(pt)
		}
		return g.ctx.StructType(fieldTypes, false)
	case sema.TypePointer:
		// A pointer's own pointee type never matters to its LLVM
		// representation - g.ptrTy is already the single opaque `ptr` type
		// this package uses everywhere a pointer is needed (string/dynamic-
		// array/func-value fields, method receivers, ...), so a real `*T`
		// value (see LANGUAGE.md's "Pointers" section) reuses it directly
		// rather than needing its own distinct LLVM pointer-to-X type.
		return g.ptrTy
	case sema.TypeCoroutine:
		// A coroutine handle is exactly llvm.coro.begin's own return type - a
		// single opaque `ptr`, reusing g.ptrTy the same way TypePointer/TypeMap
		// already do (see CODEGEN.md's "Coroutines" section).
		return g.ptrTy
	case sema.TypeMap:
		// A map's own runtime value is likewise just a single opaque `ptr` -
		// the address of its own arena-allocated control block (g.mapCtrlTy -
		// see maps.go's own top-of-file doc comment and CODEGEN.md's "Maps"
		// section), never moved once allocated. This is exactly what makes
		// assigning one map-typed variable to another share the same live
		// table (Go's own real map-is-a-reference-type behavior): copying
		// this pointer value is all an assignment/argument/return ever does.
		return g.ptrTy
	default:
		panic(fmt.Sprintf("codegen: type %s reached llvmType - only valid, checked types are supported", t))
	}
}

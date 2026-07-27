package sema

// FieldSpec is one struct field's name and checked Type, in declaration
// order - the shape ResolveStructFields returns, since StructInfo.Fields
// alone (an unordered map - see its own doc comment) can't answer "in what
// order".
type FieldSpec struct {
	Name string
	Type Type
}

// ResolveStructFields returns si's own fields' names/Types in declaration
// order, or ok=false when they aren't available (e.g. an LSP hover over a
// struct whose declaring file hasn't been analyzed). codegen.Generator
// answers this from its own precomputed structLayouts; src/lsp answers it by
// walking the declaring file's own AST + Info, which may not be the file the
// request came from (the same cross-file concern FuncSignatureText/
// StructFieldsText already handle for signatures).
type ResolveStructFields func(si *StructInfo) (fields []FieldSpec, ok bool)

// FieldLayout is one field's placement within its own struct's real memory
// layout - StructLayout's own doc comment. Align is this field's own type's
// alignment, which can differ from the struct's overall Align (its widest
// field) - the LSP's per-field hover shows this one, not the struct's.
type FieldLayout struct {
	Name   string
	Type   Type
	Offset uint64
	Size   uint64
	Align  uint64
}

// StructLayout is a struct's real, natural-alignment memory layout: total
// Size/Align, each field's own Offset/Size in declaration order, and
// Padding - bytes Size spends on alignment rather than any field's own data.
// Computed by StructLayoutOf for the LSP's CLion-style struct hover.
type StructLayout struct {
	Size    uint64
	Align   uint64
	Padding uint64
	Fields  []FieldLayout
}

// SizeAlign computes t's real natural-alignment byte size/alignment,
// recursing into a nested struct field via resolve. ok is false for a Type
// this can't size - TypeVoid, an untyped constant, a call-only pseudo-type
// (TypeMultiReturn/TypeGenerator/TypeCoroutine), a nested struct resolve
// itself couldn't resolve, or a struct that (directly or through a
// fixed-size array) contains itself by value - never a panic, since a caller
// may run this over an arbitrary struct, not only the FFI-safe subset
// codegen restricts itself to (see this function's use from
// codegen/ffi.go's own abiSizeAlign). A cyclic-by-value struct is otherwise
// unrejected elsewhere in this package (see structCopyable's own doc
// comment) and has no finite size, so ok=false is the only sound answer -
// unlike structCopyable's own cycle guard, which breaks the same recursion
// by assuming copyable instead, a choice that question can afford and this
// one can't.
//
// Every runtime shape mirrors its one real LLVM representation exactly (see
// codegen.Generator's own stringTy/funcValTy/dynArrTy/enumValTy doc
// comments), hand-written here rather than queried via LLVM's TargetData:
// src/lsp deliberately never links tinygo.org/x/go-llvm at all (see
// cmd/llvmc-lsp's own doc comment), and codegen itself can't rely on
// TargetData this early either (ffi.go's own reasoning, which predates this
// function and still applies unchanged).
func SizeAlign(t Type, resolve ResolveStructFields) (size, align uint64, ok bool) {
	return sizeAlign(t, resolve, make(map[*StructInfo]bool))
}

func sizeAlign(t Type, resolve ResolveStructFields, visiting map[*StructInfo]bool) (size, align uint64, ok bool) {
	switch t.Kind {
	case TypeI8, TypeU8, TypeBool:
		return 1, 1, true
	case TypeI16, TypeU16:
		return 2, 2, true
	case TypeI32, TypeU32, TypeF32:
		return 4, 4, true
	case TypeI64, TypeU64, TypeF64, TypePointer, TypeCString, TypeCFunc:
		return 8, 8, true
	case TypeMap:
		// A map's own value representation is a bare pointer to its
		// (arena-allocated, out-of-line) control block - see
		// codegen.Generator's own mapCtrlTy doc comment.
		return 8, 8, true
	case TypeString, TypeFunc, TypeEnum, TypeAny:
		// {ptr, i32} / {ptr, ptr} / {i32, ptr} / {dataPtr, descriptorPtr}
		// respectively - four fixed two-field shapes that each carry a real
		// pointer, so natural alignment rounds all four up to the same 16
		// bytes/align 8. See DECISIONS.md for TypeAny's own {ptr, ptr} shape.
		return 16, 8, true
	case TypeArray:
		if t.Dynamic {
			// {dataPtr, len, cap} - a dynamic array's own element type never
			// affects ITS size; the elements live out-of-line.
			return 16, 8, true
		}
		elemSize, elemAlign, ok := sizeAlign(*t.Elem, resolve, visiting)
		if !ok {
			return 0, 0, false
		}
		return roundUpToAlign(elemSize, elemAlign) * uint64(t.Size), elemAlign, true
	case TypeStruct:
		layout, ok := structLayoutOf(t.Struct, resolve, visiting)
		if !ok {
			return 0, 0, false
		}
		return layout.Size, layout.Align, true
	default:
		return 0, 0, false
	}
}

func roundUpToAlign(n, align uint64) uint64 {
	return (n + align - 1) / align * align
}

// StructLayoutOf computes si's own full layout - StructLayout's own doc
// comment - resolving its fields' declaration order/types via resolve.
func StructLayoutOf(si *StructInfo, resolve ResolveStructFields) (StructLayout, bool) {
	return structLayoutOf(si, resolve, make(map[*StructInfo]bool))
}

func structLayoutOf(si *StructInfo, resolve ResolveStructFields, visiting map[*StructInfo]bool) (StructLayout, bool) {
	// A struct already on the current recursion path means si contains
	// itself by value (see SizeAlign's own doc comment) - break the cycle
	// as "can't size" rather than recursing forever.
	if visiting[si] {
		return StructLayout{}, false
	}
	visiting[si] = true
	defer delete(visiting, si)

	fields, ok := resolve(si)
	if !ok {
		return StructLayout{}, false
	}

	var size, align, dataBytes uint64 = 0, 1, 0
	out := make([]FieldLayout, len(fields))
	for i, f := range fields {
		fSize, fAlign, ok := sizeAlign(f.Type, resolve, visiting)
		if !ok {
			return StructLayout{}, false
		}
		align = max(align, fAlign)
		offset := roundUpToAlign(size, fAlign)
		out[i] = FieldLayout{Name: f.Name, Type: f.Type, Offset: offset, Size: fSize, Align: fAlign}
		size = offset + fSize
		dataBytes += fSize
	}
	size = roundUpToAlign(size, align)

	return StructLayout{
		Size:    size,
		Align:   align,
		Padding: size - dataBytes,
		Fields:  out,
	}, true
}

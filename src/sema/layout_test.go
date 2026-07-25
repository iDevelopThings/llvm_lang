package sema

import "testing"

// fixedFields returns a ResolveStructFields that always answers si with
// fields, ignoring si itself - enough for these tests, which never need to
// distinguish between multiple StructInfos.
func fixedFields(fields []FieldSpec) ResolveStructFields {
	return func(*StructInfo) ([]FieldSpec, bool) {
		return fields, true
	}
}

// TestStructLayoutOf_PadsBetweenFieldsForAlignment covers the case CLion's
// own struct tooltip exists to show: a narrow field followed by a wider one
// forces a gap, not tight packing - {i8, i32} lands the i32 at offset 4, not
// 1, wasting 3 bytes nobody's data occupies.
func TestStructLayoutOf_PadsBetweenFieldsForAlignment(t *testing.T) {
	fields := []FieldSpec{
		{Name: "a", Type: Type{Kind: TypeI8}},
		{Name: "b", Type: Type{Kind: TypeI32}},
	}
	layout, ok := StructLayoutOf(&StructInfo{}, fixedFields(fields))
	if !ok {
		t.Fatal("StructLayoutOf returned ok=false, want true")
	}
	if layout.Size != 8 || layout.Align != 4 {
		t.Errorf("Size/Align = %d/%d, want 8/4", layout.Size, layout.Align)
	}
	if layout.Padding != 3 {
		t.Errorf("Padding = %d, want 3 (bytes 1-3, before b)", layout.Padding)
	}
	if len(layout.Fields) != 2 || layout.Fields[0].Offset != 0 || layout.Fields[1].Offset != 4 {
		t.Fatalf("Fields = %+v, want a@0, b@4", layout.Fields)
	}
}

// TestStructLayoutOf_NoPaddingWhenFieldsAlreadyAligned covers the other
// direction: {i32, i32} needs no gap at all - Padding must be exactly 0, not
// some nonzero rounding artifact.
func TestStructLayoutOf_NoPaddingWhenFieldsAlreadyAligned(t *testing.T) {
	fields := []FieldSpec{
		{Name: "x", Type: Type{Kind: TypeI32}},
		{Name: "y", Type: Type{Kind: TypeI32}},
	}
	layout, ok := StructLayoutOf(&StructInfo{}, fixedFields(fields))
	if !ok {
		t.Fatal("StructLayoutOf returned ok=false, want true")
	}
	if layout.Size != 8 || layout.Align != 4 || layout.Padding != 0 {
		t.Errorf("Size/Align/Padding = %d/%d/%d, want 8/4/0", layout.Size, layout.Align, layout.Padding)
	}
}

// TestStructLayoutOf_NestedStructRecursesAndAddsTailPadding covers a struct
// field whose own type is itself a struct: the outer layout must recurse via
// resolve for the inner one, and a trailing i8 after an 8-byte-aligned inner
// struct must still round the whole thing up to the outer alignment (tail
// padding, not just inter-field padding).
func TestStructLayoutOf_NestedStructRecursesAndAddsTailPadding(t *testing.T) {
	inner := &StructInfo{}
	resolve := func(si *StructInfo) ([]FieldSpec, bool) {
		if si == inner {
			return []FieldSpec{{Name: "p", Type: Type{Kind: TypePointer}}}, true
		}
		return []FieldSpec{
			{Name: "handle", Type: Type{Kind: TypeStruct, Struct: inner}},
			{Name: "flag", Type: Type{Kind: TypeI8}},
		}, true
	}

	layout, ok := StructLayoutOf(&StructInfo{}, resolve)
	if !ok {
		t.Fatal("StructLayoutOf returned ok=false, want true")
	}
	// inner: {ptr} = size 8, align 8. outer: handle@0 (8 bytes), flag@8 (1
	// byte) = 9 bytes of real data, rounded up to align 8 -> size 16.
	if layout.Size != 16 || layout.Align != 8 {
		t.Errorf("Size/Align = %d/%d, want 16/8", layout.Size, layout.Align)
	}
	if layout.Padding != 7 {
		t.Errorf("Padding = %d, want 7 (tail padding after flag)", layout.Padding)
	}
}

// TestSizeAlign_UnsupportedKindReturnsNotOk covers the graceful-degradation
// path a real LSP hover depends on: a type this can't size (no case in
// SizeAlign's own switch, e.g. TypeVoid) must report ok=false, never panic -
// unlike codegen's own abiSizeAlign wrapper, which is only ever called on an
// already-validated FFI-safe type.
func TestSizeAlign_UnsupportedKindReturnsNotOk(t *testing.T) {
	_, _, ok := SizeAlign(Type{Kind: TypeVoid}, fixedFields(nil))
	if ok {
		t.Error("SizeAlign(TypeVoid) returned ok=true, want false")
	}
}

// TestStructLayoutOf_UnresolvableFieldPropagatesNotOk covers a struct whose
// resolve callback itself fails (e.g. an LSP hover over a struct declared in
// a file with no analysis yet) - StructLayoutOf must report ok=false rather
// than a wrong/partial layout.
func TestStructLayoutOf_UnresolvableFieldPropagatesNotOk(t *testing.T) {
	resolve := func(*StructInfo) ([]FieldSpec, bool) { return nil, false }
	_, ok := StructLayoutOf(&StructInfo{}, resolve)
	if ok {
		t.Error("StructLayoutOf returned ok=true for an unresolvable struct, want false")
	}
}

// TestStructLayoutOf_SelfReferentialStructReportsNotOkInsteadOfHanging
// covers a real hazard: a struct that contains itself by value (`struct A {
// a A }`) is never rejected elsewhere in sema (see structCopyable's own doc
// comment) - so StructLayoutOf must break the cycle itself and report
// ok=false, not recurse until the stack overflows. Without the visited-set
// guard in structLayoutOf/sizeAlign, this test hangs/crashes instead of
// failing cleanly.
func TestStructLayoutOf_SelfReferentialStructReportsNotOkInsteadOfHanging(t *testing.T) {
	self := &StructInfo{}
	resolve := func(si *StructInfo) ([]FieldSpec, bool) {
		return []FieldSpec{{Name: "a", Type: Type{Kind: TypeStruct, Struct: self}}}, true
	}
	_, ok := StructLayoutOf(self, resolve)
	if ok {
		t.Error("StructLayoutOf returned ok=true for a self-referential struct, want false")
	}
}

// TestStructLayoutOf_MutuallyReferentialStructsReportNotOk is the two-struct
// version of the same cycle: A contains B by value, B contains A by value -
// neither alone is self-referential, but the pair cycles just the same.
func TestStructLayoutOf_MutuallyReferentialStructsReportNotOk(t *testing.T) {
	a := &StructInfo{}
	b := &StructInfo{}
	resolve := func(si *StructInfo) ([]FieldSpec, bool) {
		if si == a {
			return []FieldSpec{{Name: "b", Type: Type{Kind: TypeStruct, Struct: b}}}, true
		}
		return []FieldSpec{{Name: "a", Type: Type{Kind: TypeStruct, Struct: a}}}, true
	}
	_, ok := StructLayoutOf(a, resolve)
	if ok {
		t.Error("StructLayoutOf returned ok=true for mutually-referential structs, want false")
	}
}

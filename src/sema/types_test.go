package sema

import "testing"

func TestType_Underlying(t *testing.T) {
	structTy := Type{Kind: TypeStruct, Struct: &StructInfo{}}
	ptrTy := Type{Kind: TypePointer, Elem: &structTy}

	if got := ptrTy.Underlying(); !got.Equal(structTy) {
		t.Errorf("Underlying() of a pointer = %+v, want the pointee %+v", got, structTy)
	}
	if got := structTy.Underlying(); !got.Equal(structTy) {
		t.Errorf("Underlying() of a non-pointer = %+v, want t unchanged (%+v)", got, structTy)
	}
}

//===- intrinsics_test.go - Tests for the intrinsic declaration API -------===//
//
// Part of the LLVM Project, under the Apache License v2.0 with LLVM Exceptions.
// See https://llvm.org/LICENSE.txt for license information.
// SPDX-License-Identifier: Apache-2.0 WITH LLVM-exception
//
//===----------------------------------------------------------------------===//
//
// This file tests LookupIntrinsicID/IntrinsicIsOverloaded/
// Module.IntrinsicDeclaration/Context.IntrinsicType/
// Module.IntrinsicOverloadedName (ir.go) - the generic mechanism for
// declaring any LLVM intrinsic by name, needed for intrinsic families (like
// the coroutine intrinsics - https://llvm.org/docs/Coroutines.html - this
// was added to support) that have no dedicated llvm-c header of their own,
// unlike e.g. Orc's.
//
//===----------------------------------------------------------------------===//

package llvm

import "testing"

// TestLookupIntrinsicIDUnknownName checks the documented "no match" case
// returns 0, not some other sentinel.
func TestLookupIntrinsicIDUnknownName(t *testing.T) {
	if id := LookupIntrinsicID("not.a.real.intrinsic"); id != 0 {
		t.Errorf("expected 0 for an unknown name, got %d", id)
	}
}

// TestIntrinsicDeclarationNonOverloaded declares a fixed-signature
// coroutine intrinsic (llvm.coro.begin) and checks LLVM fills in its real
// signature and attributes itself - the caller supplies nothing but the ID,
// unlike hand-declaring a plain extern function of the same name.
func TestIntrinsicDeclarationNonOverloaded(t *testing.T) {
	ctx := NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("test")
	defer mod.Dispose()

	id := LookupIntrinsicID("llvm.coro.begin")
	if id == 0 {
		t.Fatal("llvm.coro.begin did not resolve to a real intrinsic ID")
	}
	if IntrinsicIsOverloaded(id) {
		t.Fatal("llvm.coro.begin should not be overloaded")
	}

	fn := mod.IntrinsicDeclaration(id, nil)
	if fn.IsNil() {
		t.Fatal("IntrinsicDeclaration returned a nil Value")
	}
	if got := fn.Name(); got != "llvm.coro.begin" {
		t.Errorf("expected name %q, got %q", "llvm.coro.begin", got)
	}
	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("module verification failed: %v\n%s", err, mod.String())
	}
}

// TestIntrinsicDeclarationOverloaded declares an overloaded coroutine
// intrinsic (llvm.coro.size, whose real name/signature depends on the
// integer width the caller wants - i32 or i64), checking that paramTypes
// actually selects the requested overload and that the resulting name/type
// reflect it.
func TestIntrinsicDeclarationOverloaded(t *testing.T) {
	ctx := NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("test")
	defer mod.Dispose()

	id := LookupIntrinsicID("llvm.coro.size")
	if id == 0 {
		t.Fatal("llvm.coro.size did not resolve to a real intrinsic ID")
	}
	if !IntrinsicIsOverloaded(id) {
		t.Fatal("llvm.coro.size should be overloaded")
	}

	i64 := ctx.Int64Type()
	fn := mod.IntrinsicDeclaration(id, []Type{i64})
	if got := fn.Name(); got != "llvm.coro.size.i64" {
		t.Errorf("expected name %q, got %q", "llvm.coro.size.i64", got)
	}

	if got := mod.IntrinsicOverloadedName(id, []Type{i64}); got != "llvm.coro.size.i64" {
		t.Errorf("IntrinsicOverloadedName: expected %q, got %q", "llvm.coro.size.i64", got)
	}

	typ := ctx.IntrinsicType(id, []Type{i64})
	if got := typ.ReturnType(); got.C != i64.C {
		t.Errorf("expected llvm.coro.size.i64 to return i64, got a different type")
	}

	if err := VerifyModule(mod, ReturnStatusAction); err != nil {
		t.Fatalf("module verification failed: %v\n%s", err, mod.String())
	}
}

// TestCoroutinePresplitAttributeResolves checks that AttributeKindID
// resolves "presplitcoroutine" - the enum attribute a function must carry
// before LLVM's coroutine-splitting passes will touch it
// (https://llvm.org/docs/Coroutines.html#coroutine-transformation) - to a
// real, non-zero ID under this LLVM version. AttributeKindID itself already
// existed; this only confirms this specific, less common attribute name is
// actually registered, since a typo or a renamed attribute across LLVM
// versions would otherwise silently resolve to 0 (a valid-looking call that
// creates a meaningless attribute) rather than a build/link error.
func TestCoroutinePresplitAttributeResolves(t *testing.T) {
	if id := AttributeKindID("presplitcoroutine"); id == 0 {
		t.Fatal("presplitcoroutine did not resolve to a real enum attribute kind")
	}
}

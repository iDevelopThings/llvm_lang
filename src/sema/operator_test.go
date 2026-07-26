package sema

import (
	"testing"
)

// --- struct operator overloading (see LANGUAGE.md's "Operator overloading" section) ---

const vector2OperatorsSrc = "struct Vector2 {\n" +
	"\tx f64\n" +
	"\ty f64\n\n" +
	"\toperator *(scalar f64) Vector2 {\n" +
	"\t\treturn Vector2{this.x * scalar, this.y * scalar}\n" +
	"\t}\n" +
	"\toperator +(other Vector2) Vector2 {\n" +
	"\t\treturn Vector2{this.x + other.x, this.y + other.y}\n" +
	"\t}\n" +
	"\toperator -() Vector2 {\n" +
	"\t\treturn Vector2{-this.x, -this.y}\n" +
	"\t}\n" +
	"}\n"

// TestOperatorOverloadResolvesBinaryByArgType covers the core positive
// case: `v * 2.0` resolves to Vector2's own `operator *(scalar f64)`
// overload, recorded directly on the whole BinaryExpr node's own Info.Refs
// entry (there's no callee child the way a CallExpr has), and yields
// Vector2 (the overload's own declared return type), not the numeric
// fallback.
func TestOperatorOverloadResolvesBinaryByArgType(t *testing.T) {
	tree, info := checkSrc(t, vector2OperatorsSrc+
		"func f() f64 {\n\tv := Vector2{1, 2}\n\tscaled := v * 2.0\n\treturn scaled.x\n}\n")
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)
	scaledDecl := tree.Child(body, 1)
	mulExpr := tree.Child(scaledDecl, 1)

	sym, ok := info.Refs[mulExpr]
	if !ok {
		t.Fatal("expected the `*` use to resolve to an operator overload")
	}
	if sym.Kind != SymOperator {
		t.Fatalf("resolved to kind %s, want operator", sym.Kind)
	}
	structInfo := info.Structs["Vector2"]
	if want := structInfo.Operators["*"].Binary[0].Symbol; sym != want {
		t.Errorf("resolved to %+v, want Vector2's own operator * overload %+v", sym, want)
	}
	if got := info.Types[mulExpr]; got.Kind != TypeStruct || got.Struct != structInfo {
		t.Errorf("Types[mulExpr] = %v, want Vector2", got)
	}
}

// TestOperatorOverloadResolvesUnary covers `-v` resolving to Vector2's own
// unary `operator -()` overload.
func TestOperatorOverloadResolvesUnary(t *testing.T) {
	tree, info := checkSrc(t, vector2OperatorsSrc+
		"func f() f64 {\n\tv := Vector2{1, 2}\n\tneg := -v\n\treturn neg.x\n}\n")
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)
	negDecl := tree.Child(body, 1)
	unaryExpr := tree.Child(negDecl, 1)

	sym, ok := info.Refs[unaryExpr]
	if !ok {
		t.Fatal("expected the unary `-` use to resolve to an operator overload")
	}
	structInfo := info.Structs["Vector2"]
	if want := structInfo.Operators["-"].Unary; sym != want {
		t.Errorf("resolved to %+v, want Vector2's own unary operator - overload %+v", sym, want)
	}
	if got := info.Types[unaryExpr]; got.Kind != TypeStruct || got.Struct != structInfo {
		t.Errorf("Types[unaryExpr] = %v, want Vector2", got)
	}
}

// vector2DualMulSrc declares two `operator *` overloads distinguished only
// by their own parameter type (f64 vs Vector2) - both legal, coexisting,
// each independently callable (see LANGUAGE.md's "Operator overloading"
// section: this is the one deliberate divergence from a constructor's
// count-only overload rule).
const vector2DualMulSrc = "struct Vector2 {\n" +
	"\tx f64\n" +
	"\ty f64\n\n" +
	"\toperator *(scalar f64) Vector2 {\n" +
	"\t\treturn Vector2{this.x * scalar, this.y * scalar}\n" +
	"\t}\n" +
	"\toperator *(other Vector2) f64 {\n" +
	"\t\treturn this.x * other.x + this.y * other.y\n" +
	"\t}\n" +
	"}\n"

// TestOperatorOverloadCoexistingParamTypesEachResolveIndependently covers
// two `operator *` overloads on the same struct, discriminated purely by
// their own declared parameter type: `v * 2.0` (a float) and `v * w` (a
// Vector2) each resolve to their own distinct overload at their own call
// site, with the right result type.
func TestOperatorOverloadCoexistingParamTypesEachResolveIndependently(t *testing.T) {
	tree, info := checkSrc(t, vector2DualMulSrc+
		"func f() f64 {\n"+
		"\tv := Vector2{1, 2}\n"+
		"\tw := Vector2{3, 4}\n"+
		"\tscaled := v * 2.0\n"+
		"\tdotp := v * w\n"+
		"\treturn scaled.x + dotp\n"+
		"}\n")
	structInfo := info.Structs["Vector2"]
	set := structInfo.Operators["*"]
	if len(set.Binary) != 2 {
		t.Fatalf("Operators[\"*\"].Binary has %d entries, want 2", len(set.Binary))
	}

	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)

	scaledDecl := tree.Child(body, 2)
	scaledMul := tree.Child(scaledDecl, 1)
	scaledSym := info.Refs[scaledMul]
	if got := info.Types[scaledMul]; got.Kind != TypeStruct || got.Struct != structInfo {
		t.Errorf("Types[v * 2.0] = %v, want Vector2", got)
	}

	dotpDecl := tree.Child(body, 3)
	dotpMul := tree.Child(dotpDecl, 1)
	dotpSym := info.Refs[dotpMul]
	if got := info.Types[dotpMul]; got.Kind != TypeF64 {
		t.Errorf("Types[v * w] = %v, want f64", got)
	}

	if scaledSym == dotpSym {
		t.Error("v * 2.0 and v * w resolved to the same overload symbol, want two distinct ones")
	}
}

// TestOperatorDuplicateUnaryIsError covers rejecting two unary `operator -`
// overloads on one struct - a structural error raised at struct-declaration
// time (declareOperator, resolve.go), mirroring
// TestConstructorDuplicateArityIsError's own shape (constructor_test.go): a
// Resolve-phase diagnostic, not a Check-phase one.
func TestOperatorDuplicateUnaryIsError(t *testing.T) {
	src := "struct Vector2 {\n\tx f64\n\n" +
		"\toperator -() Vector2 {\n\t\treturn Vector2{-this.x}\n\t}\n" +
		"\toperator -() Vector2 {\n\t\treturn Vector2{-this.x}\n\t}\n" +
		"}\n"
	diags := expectResolveErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "already has a unary operator - overload")
}

// TestOperatorDuplicateBinarySameParamTypeIsError covers rejecting two
// `operator *` overloads sharing the exact same declared parameter type
// (both `f64`) - the binary-arity counterpart to
// TestOperatorDuplicateUnaryIsError, also a Resolve-phase diagnostic.
func TestOperatorDuplicateBinarySameParamTypeIsError(t *testing.T) {
	src := "struct Vector2 {\n\tx f64\n\n" +
		"\toperator *(scalar f64) Vector2 {\n\t\treturn Vector2{this.x * scalar}\n\t}\n" +
		"\toperator *(other f64) Vector2 {\n\t\treturn Vector2{this.x * other}\n\t}\n" +
		"}\n"
	diags := expectResolveErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "already has an operator * overload taking f64")
}

// TestOperatorDuplicateBinaryTypeSynonymIsError covers the case
// declareOperator's own Resolve-time textual comparison cannot see at all:
// `int` and `i32` are spelled differently but are the exact same real Type
// (see LANGUAGE.md's "Numeric types" section) - so a `+` overload taking
// each is accepted at Resolve time (ParamTypeText differs), but must still
// be caught once Check has a real Type to compare
// (checkOperatorOverloadDuplicates, typecheck.go) - otherwise the second
// overload is silently, permanently unreachable with no diagnostic ever
// naming the collision.
func TestOperatorDuplicateBinaryTypeSynonymIsError(t *testing.T) {
	src := "struct Wrapper {\n\tv i32\n\n" +
		"\toperator +(x int) Wrapper {\n\t\treturn Wrapper{this.v + x}\n\t}\n" +
		"\toperator +(x i32) Wrapper {\n\t\treturn Wrapper{this.v + x + 1000}\n\t}\n" +
		"}\n"
	diags := expectCheckErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "struct Wrapper already has an operator + overload taking int")
}

// TestOperatorUnsupportedBinaryTokenIsError covers rejecting a binary
// operator token outside this round's narrow supported set (+ - * / only -
// see LANGUAGE.md's "Operator overloading" section) with a real diagnostic
// naming the operator, rather than silently accepting it as dead,
// unreachable code.
func TestOperatorUnsupportedBinaryTokenIsError(t *testing.T) {
	src := "struct Vector2 {\n\tx f64\n\n" +
		"\toperator %(v f64) f64 {\n\t\treturn v\n\t}\n" +
		"}\n"
	diags := expectResolveErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "only supported for + - * / (binary) and - (unary)")
}

// TestOperatorUnsupportedUnaryTokenIsError covers the unary-position
// counterpart: only `-` is unary-overloadable this round, so a
// zero-parameter `operator +()` is rejected too.
func TestOperatorUnsupportedUnaryTokenIsError(t *testing.T) {
	src := "struct Vector2 {\n\tx f64\n\n" +
		"\toperator +() Vector2 {\n\t\treturn this\n\t}\n" +
		"}\n"
	diags := expectResolveErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "unary operator overloading is only supported for -")
}

// TestOperatorTooManyParamsIsError covers rejecting an operator declaring
// more than one parameter - an operator overload is either unary (0
// params) or binary (1 param), never anything else.
func TestOperatorTooManyParamsIsError(t *testing.T) {
	src := "struct Vector2 {\n\tx f64\n\n" +
		"\toperator +(a f64, b f64) Vector2 {\n\t\treturn this\n\t}\n" +
		"}\n"
	diags := expectResolveErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "must declare 0 (unary) or 1 (binary) parameters, got 2")
}

// TestOperatorNoMatchingBinaryOverloadIsError covers `v * o` where o's type
// matches none of Vector2's own declared `*` overloads - a clean diagnostic
// naming the actual problem (a missing overload for that argument type),
// not the pre-existing "requires numeric operands" wording, which would
// misdescribe why a struct LHS genuinely failed here.
func TestOperatorNoMatchingBinaryOverloadIsError(t *testing.T) {
	src := vector2OperatorsSrc +
		"struct Other {\n\tz int\n}\n" +
		"func f() {\n\tv := Vector2{1, 2}\n\to := Other{1}\n\tbad := v * o\n}\n"
	diags := expectCheckErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "no operator * overload on Vector2 for argument type Other")
}

// TestOperatorBinaryOnStructWithNoOperatorsAtAllUsesNewWording covers the
// same new wording firing even when the struct declares no operator
// overloads at all for that token (as opposed to declaring some, just none
// matching) - both are "no matching overload", the same actual problem.
func TestOperatorBinaryOnStructWithNoOperatorsAtAllUsesNewWording(t *testing.T) {
	src := "struct Plain {\n\tx int\n}\n" +
		"func f() {\n\tp := Plain{1}\n\tbad := p * p\n}\n"
	diags := expectCheckErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "no operator * overload on Plain for argument type Plain")
}

// TestOperatorReverseOrderScalarOnLeftIsRejected covers the deliberate
// left-operand-only dispatch restriction (see LANGUAGE.md's "Operator
// overloading" section): `2.0 * v` (the scalar on the left) is rejected,
// same as before this feature existed, with the existing "requires numeric
// operands" wording - not "fixed" by adding commutative resolution. From
// this check's own perspective the LHS (2.0) genuinely isn't a struct, so
// it never even looks at Vector2's own operator overloads.
func TestOperatorReverseOrderScalarOnLeftIsRejected(t *testing.T) {
	src := vector2OperatorsSrc +
		"func f() {\n\tv := Vector2{1, 2}\n\tbad := 2.0 * v\n}\n"
	diags := expectCheckErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "operator * requires numeric operands")
}

// TestOperatorNoUnaryOverloadFallsBackToNumericDiagnostic is the regression
// case explicitly called for in LANGUAGE.md: a struct declaring NO operator
// overloads at all still falls through to the exact pre-existing "operator
// - not defined for" diagnostic when negated, completely unchanged by this
// feature's own fallback path.
func TestOperatorNoUnaryOverloadFallsBackToNumericDiagnostic(t *testing.T) {
	src := "struct Plain {\n\tx int\n}\n" +
		"func f() {\n\tp := Plain{1}\n\tbad := -p\n}\n"
	diags := expectCheckErrors(t, src, 1)
	wantDiag(t, diags.All()[0].Msg, "operator - not defined for Plain")
}

// TestOperatorOverloadUsableWithUntypedIntLiteral covers an untyped integer
// literal (not just an untyped float, `2.0`) adapting to a declared f64
// parameter through the same untyped-constant rule an ordinary numeric
// binary operator already applies (resolveNumericOperands) - operator
// overload resolution reuses that exact adaptation (operandTypeMatches),
// not a separate, narrower rule.
func TestOperatorOverloadUsableWithUntypedIntLiteral(t *testing.T) {
	checkSrc(t, vector2OperatorsSrc+
		"func f() f64 {\n\tv := Vector2{1, 2}\n\tscaled := v * 2\n\treturn scaled.x\n}\n")
}

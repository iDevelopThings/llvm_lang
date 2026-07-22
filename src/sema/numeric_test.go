package sema

import "testing"

// --- new primitive widths, and int/i32 being the same type ---

func TestI8I16I32I64F32F64AreValidTypeNames(t *testing.T) {
	checkSrc(t, "var a i8 = 1\nvar b i16 = 1\nvar c i32 = 1\nvar d i64 = 1\nvar e f32 = 1.0\nvar f f64 = 1.0\n")
}

func TestIntAndI32AreTheSameType(t *testing.T) {
	// A variable declared `i32` can be assigned to/from one declared `int`
	// with no conversion - they're the exact same Type (see TypeInt's doc
	// comment in types.go).
	tree, info := checkSrc(t, "var a int = 1\nvar b i32 = a\n")
	decl := tree.Children(tree.Root)[1]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeI32 {
		t.Errorf("Types[init] = %v, want i32", got)
	}
}

func TestDifferentIntWidthsCannotMixWithoutConversion(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b i64 = 1\n\tvar c i64 = a + b\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestDifferentFloatWidthsCannotMixWithoutConversion(t *testing.T) {
	src := "func f() {\n\tvar a f32 = 1.0\n\tvar b f64 = 1.0\n\tvar c f64 = a + b\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- untyped constant literal typing ---

func TestBareIntLiteralIsUntypedUntilResolved(t *testing.T) {
	tree, info := checkSrc(t, "var a i64 = 5\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeI64 {
		t.Errorf("Types[init] = %v, want i64 (an untyped int literal should adapt to its declared context)", got)
	}
}

func TestBareFloatLiteralIsUntypedUntilResolved(t *testing.T) {
	tree, info := checkSrc(t, "var a f32 = 5.0\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeF32 {
		t.Errorf("Types[init] = %v, want f32", got)
	}
}

// --- untyped-constant defaulting (no declared type) ---

func TestUntypedIntDefaultsToI32WithNoDeclaredType(t *testing.T) {
	tree, info := checkSrc(t, "var a = 5\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeI32 {
		t.Errorf("Types[init] = %v, want i32 (Go's own untyped-int default)", got)
	}
}

func TestUntypedFloatDefaultsToF64WithNoDeclaredType(t *testing.T) {
	tree, info := checkSrc(t, "var a = 5.0\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeF64 {
		t.Errorf("Types[init] = %v, want f64 (Go's own untyped-float default)", got)
	}
}

func TestShortVarDeclUntypedIntDefaultsToI32(t *testing.T) {
	src := "func f() {\n\ta := 5\n\tvar b i32 = a\n}\n"
	checkSrc(t, src)
}

func TestShortVarDeclUntypedFloatDefaultsToF64(t *testing.T) {
	src := "func f() {\n\ta := 5.0\n\tvar b f64 = a\n}\n"
	checkSrc(t, src)
}

// --- untyped-constant adapting to a declared type ---

func TestUntypedIntAdaptsToI8(t *testing.T) {
	checkSrc(t, "var a i8 = 5\n")
}

func TestUntypedIntAdaptsToFloat(t *testing.T) {
	// Go's own rule: an untyped int constant can adapt to a float context -
	// `var a f64 = 5` is fine, same as real Go.
	checkSrc(t, "var a f64 = 5\n")
}

func TestUntypedFloatCannotAdaptToInt(t *testing.T) {
	// The one direction Go itself also rejects (silent truncation) -
	// `var a int = 5.5` - see AGENTS.md's Types section.
	expectCheckErrors(t, "var a int = 5.5\n", 1)
}

func TestUntypedFloatCannotAdaptToI64(t *testing.T) {
	expectCheckErrors(t, "var a i64 = 5.5\n", 1)
}

// --- binary operator: untyped operand meets a concrete one ---

func TestUntypedIntOperandAdaptsToConcreteIntWidthInBinaryExpr(t *testing.T) {
	src := "func f() {\n\tvar a i64 = 1\n\tvar b i64 = a + 5\n}\n"
	checkSrc(t, src)
}

func TestUntypedIntOperandAdaptsToConcreteFloatInBinaryExpr(t *testing.T) {
	src := "func f() {\n\tvar a f64 = 1.5\n\tvar b f64 = a + 5\n}\n"
	checkSrc(t, src)
}

func TestUntypedFloatOperandCannotAdaptToConcreteIntInBinaryExpr(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b i32 = a + 5.5\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestConcreteIntWidthMismatchInBinaryExprIsError(t *testing.T) {
	// Two already-concretely-typed values of different widths still can't
	// mix without an explicit conversion, even though each alone is a valid
	// int type - see AGENTS.md's Types section.
	src := "func f() {\n\tvar a i32 = 1\n\tvar b i64 = 2\n\tvar c i64 = b + a\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- binary operator: two untyped operands combining ---

func TestTwoUntypedIntOperandsCombineAndDeferResolution(t *testing.T) {
	// `1 + 2` is untyped-int + untyped-int - the result should still adapt
	// freely to any numeric declared type, including a narrow one.
	checkSrc(t, "var a i8 = 1 + 2\n")
	checkSrc(t, "var a f32 = 1 + 2\n")
}

func TestTwoUntypedOperandsWithAFloatLookingLiteralCombineAsUntypedFloat(t *testing.T) {
	// `1 + 2.5` combines an untyped-int and an untyped-float literal - the
	// result is untyped-float (deferred), so it can adapt to a float
	// declared type but NOT to an int one (would truncate the float side).
	checkSrc(t, "var a f64 = 1 + 2.5\n")
	expectCheckErrors(t, "var a i32 = 1 + 2.5\n", 1)
}

func TestNestedArithmeticOfUntypedOperandsResolvesEveryLeaf(t *testing.T) {
	// (1 + 2) * 3 - every literal several levels deep in the tree must end
	// up retyped to the declared context, not just the outermost node.
	checkSrc(t, "var a i64 = (1 + 2) * 3\n")
}

func TestUnaryMinusOnUntypedLiteralStaysUntyped(t *testing.T) {
	checkSrc(t, "var a i16 = -5\n")
	checkSrc(t, "var a f32 = -5.5\n")
	expectCheckErrors(t, "var a i16 = -5.5\n", 1)
}

// --- bitwise/%% operators reject float ---

func TestBitwiseOperatorsRejectFloat(t *testing.T) {
	expectCheckErrors(t, "var a f64 = 1.0\nvar b f64 = 2.0\nvar c f64 = a & b\n", 1)
}

func TestModuloRejectsFloat(t *testing.T) {
	expectCheckErrors(t, "var a f64 = 1.0\nvar b f64 = 2.0\nvar c f64 = a % b\n", 1)
}

func TestBitwiseOperatorsWorkAcrossIntWidths(t *testing.T) {
	checkSrc(t, "var a i64 = 1 & 2\n")
	checkSrc(t, "var a i64 = 1 | 2\n")
	checkSrc(t, "var a i64 = 1 ^ 2\n")
}

// --- comparisons: untyped operands default when there's no external target ---

func TestComparisonOfTwoUntypedIntLiteralsIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tif 1 < 2 {\n\t}\n}\n")
}

func TestComparisonOfTwoUntypedFloatLiteralsIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tif 1.5 < 2.5 {\n\t}\n}\n")
}

func TestComparisonOfUntypedAgainstConcreteWidth(t *testing.T) {
	src := "func f() {\n\tvar a i64 = 5\n\tif a > 3 {\n\t}\n}\n"
	checkSrc(t, src)
}

func TestComparisonOfDifferentConcreteWidthsIsError(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 5\n\tvar b i64 = 3\n\tif a > b {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- function call argument context ---

func TestUntypedArgumentAdaptsToParamType(t *testing.T) {
	src := "func f(x i64) {\n}\nfunc g() {\n\tf(5)\n}\n"
	checkSrc(t, src)
}

func TestUntypedFloatArgumentCannotAdaptToIntParam(t *testing.T) {
	src := "func f(x i32) {\n}\nfunc g() {\n\tf(5.5)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestConcreteArgumentWidthMismatchIsError(t *testing.T) {
	src := "func f(x i64) {\n}\nfunc g() {\n\tvar a i32 = 1\n\tf(a)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- return statement context ---

func TestUntypedReturnValueAdaptsToDeclaredReturnType(t *testing.T) {
	checkSrc(t, "func f() i64 {\n\treturn 5\n}\n")
	checkSrc(t, "func f() f64 {\n\treturn 5\n}\n")
}

func TestUntypedFloatReturnValueCannotAdaptToIntReturnType(t *testing.T) {
	expectCheckErrors(t, "func f() i32 {\n\treturn 5.5\n}\n", 1)
}

func TestConcreteReturnTypeMismatchIsError(t *testing.T) {
	src := "func f() i64 {\n\tvar a i32 = 1\n\treturn a\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- array/composite literal element context ---

func TestUntypedArrayElementsAdaptToDeclaredElementType(t *testing.T) {
	checkSrc(t, "var a [3]i64 = [3]i64{1, 2, 3}\n")
	checkSrc(t, "var a [3]f64 = [3]f64{1, 2, 3}\n")
}

func TestUntypedFloatArrayElementCannotAdaptToIntElementType(t *testing.T) {
	expectCheckErrors(t, "var a [2]i32 = [2]i32{1, 2.5}\n", 1)
}

func TestUntypedStructFieldElementsAdaptToDeclaredFieldType(t *testing.T) {
	src := "struct Point {\n\tx i64\n\ty f64\n}\n" +
		"var p Point = Point{1, 2}\n"
	checkSrc(t, src)
}

// --- ++/--/compound-assign generalized beyond int ---

func TestIncDecWorksOnAnyNumericWidth(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a i8 = 1\n\ta++\n}\n")
	checkSrc(t, "func f() {\n\tvar a i64 = 1\n\ta--\n}\n")
	checkSrc(t, "func f() {\n\tvar a f64 = 1.0\n\ta++\n}\n")
}

func TestCompoundAssignWorksOnFloat(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a f64 = 1.0\n\ta += 2.5\n\ta *= 2.0\n}\n")
}

func TestCompoundAssignRejectsWidthMismatch(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b i64 = 2\n\ta += b\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- explicit numeric conversions ---

func TestConversionBetweenIntWidths(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b i64 = i64(a)\n}\n"
	checkSrc(t, src)
}

func TestConversionBetweenFloatWidths(t *testing.T) {
	src := "func f() {\n\tvar a f32 = 1.0\n\tvar b f64 = f64(a)\n}\n"
	checkSrc(t, src)
}

func TestConversionBetweenIntAndFloat(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b f64 = f64(a)\n\tvar c i32 = i32(b)\n}\n"
	checkSrc(t, src)
}

func TestConversionOfALiteral(t *testing.T) {
	checkSrc(t, "var a i64 = i64(5)\n")
}

func TestConversionRecordsTargetTypeOnTheCallExprNode(t *testing.T) {
	tree, info := checkSrc(t, "var a i64 = i64(5)\n")
	decl := tree.Children(tree.Root)[0]
	call := tree.Child(decl, 2)
	if got := info.Types[call]; got.Kind != TypeI64 {
		t.Errorf("Types[conversion call] = %v, want i64", got)
	}
}

func TestConversionWrongArgCountIsError(t *testing.T) {
	expectCheckErrors(t, "var a i64 = i64(1, 2)\n", 1)
	expectCheckErrors(t, "func f() {\n\ti64()\n}\n", 1)
}

func TestConversionOfNonNumericIsError(t *testing.T) {
	expectCheckErrors(t, "var a i64 = i64(\"hello\")\n", 1)
}

func TestConversionToNonNumericTypeIsError(t *testing.T) {
	src := "struct Point {\n\tx int\n\ty int\n}\n" +
		"func f() {\n\tvar a int = 1\n\tPoint(a)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestSameTypeConversionIsFine(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b i32 = i32(a)\n}\n"
	checkSrc(t, src)
}

// --- array index / array size: bare literal defaults to i32 ---

func TestArrayIndexBareLiteralDefaultsToI32(t *testing.T) {
	src := "func f() {\n\tvar a [3]int = [3]int{1, 2, 3}\n\tvar b int = a[0]\n}\n"
	checkSrc(t, src)
}

func TestArrayIndexWithConcreteI64VariableIsError(t *testing.T) {
	// Only plain int (i32) indices are accepted, even though the index
	// value is a perfectly valid numeric type - no implicit narrowing.
	src := "func f() {\n\tvar a [3]int = [3]int{1, 2, 3}\n\tvar i i64 = 0\n\tvar b int = a[i]\n}\n"
	expectCheckErrors(t, src, 1)
}

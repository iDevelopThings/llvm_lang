// This file implements codegen's compile-time constant evaluator, used for
// top-level `var` initializers (see codegen.go's genGlobalVarDecl and
// CODEGEN.md's "Global var initializers" section): whenever an initializer
// happens to be foldable at compile time - literals, parenthesization, unary
// +/-/!, binary arithmetic/comparison/logical/string-concatenation, and
// struct/(fixed-size-)array composite literals built entirely from constants
// - it's folded directly into the global's own LLVM initializer, exactly as
// before this file ever had a "non-constant" counterpart. It is deliberately
// more capable than sema/typecheck.go's constArraySize (which only ever needs
// a bare integer literal for an array size).
//
// Anything else (a reference to another variable, a function call, `this`, a
// member/index expression, a dynamic-array/slice literal) is not a constant
// expression this evaluator can fold - genGlobalVarDecl routes those through
// a synthesized init function instead (see globalinit.go), so constExpr is
// only ever invoked once isConstFoldable (below) has already confirmed the
// whole initializer is structurally constant-shaped; every diagnostic
// constExpr still reports (an out-of-range literal, division by zero, an
// unsupported operator) is therefore a real error in an expression that
// genuinely looks constant, not "this isn't a constant at all".
package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// constExpr evaluates n as a compile-time constant, reporting a diagnostic
// and returning ok=false if it isn't one.
func (g *Generator) constExpr(n ast.NodeIndex) (llvm.Value, bool) {
	// A string-typed constant is computed as a Go string first (constant
	// folding string concatenation needs the actual text, not an LLVM
	// value - see constStringText), then turned into a {ptr,len} value at
	// the very end, same as any other string literal.
	if t := g.info.Types[n]; t.Kind == sema.TypeString {
		text, ok := g.constStringText(n)
		if !ok {
			g.errorAt(n, "global initializer must be a compile-time constant expression")
			return llvm.Value{}, false
		}
		return g.constStringValue(text), true
	}

	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.NumberLit:
		return g.genNumberLit(n), true
	case enums.NodeKinds.BoolLit:
		return g.genBoolLit(n), true
	case enums.NodeKinds.ParenExpr:
		return g.constExpr(g.tree.Child(n, 0))
	case enums.NodeKinds.UnaryExpr:
		return g.constUnaryExpr(n)
	case enums.NodeKinds.BinaryExpr:
		return g.constBinaryExpr(n)
	case enums.NodeKinds.CompositeLit:
		return g.constCompositeLit(n)
	default:
		g.errorAt(n, "global initializer must be a compile-time constant expression")
		return llvm.Value{}, false
	}
}

// constStringText recursively folds n down to a plain Go string - a string
// literal, or `+`-concatenation of constant strings (arbitrarily nested).
func (g *Generator) constStringText(n ast.NodeIndex) (string, bool) {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.StringLit:
		return g.tree.File.StringValue(g.tree.Nodes[n].Tok), true
	case enums.NodeKinds.ParenExpr:
		return g.constStringText(g.tree.Child(n, 0))
	case enums.NodeKinds.BinaryExpr:
		if g.tree.Text(n) != "+" {
			return "", false
		}
		l, ok1 := g.constStringText(g.tree.Child(n, 0))
		r, ok2 := g.constStringText(g.tree.Child(n, 1))
		if !ok1 || !ok2 {
			return "", false
		}
		return l + r, true
	default:
		return "", false
	}
}

func (g *Generator) constUnaryExpr(n ast.NodeIndex) (llvm.Value, bool) {
	operand, ok := g.constExpr(g.tree.Child(n, 0))
	if !ok {
		return llvm.Value{}, false
	}
	t := g.info.Types[n]
	switch g.tree.Text(n) {
	case "-":
		if t.IsFloatKind() {
			f, _ := operand.DoubleValue()
			return llvm.ConstFloat(g.llvmType(t), -f), true
		}
		if t.IsUnsigned() {
			// A constant context is always a constant negation - out of range
			// for any unsigned type (see genUnaryExpr's runtime counterpart).
			g.errorAt(n, "negation of unsigned constant is out of range for %s", t)
			return llvm.Value{}, false
		}
		return g.constIntOfType(t, -operand.SExtValue()), true
	case "!":
		return g.constBool(operand.ZExtValue() == 0), true
	default:
		g.errorAt(n, "unsupported constant unary operator")
		return llvm.Value{}, false
	}
}

// constBinaryExpr folds a BinaryExpr whose operands are already known (by
// the caller, constExpr) not to be strings - numeric (any int width or float
// width) arithmetic/comparison, or bool logical/comparison, matching the
// same operator rules sema/typecheck.go's checkBinaryExpr enforces (see
// AGENTS.md's Operators section). Every arithmetic result is folded at the
// expression's own already-resolved width/kind (g.info.Types[n], exactly as
// sema determined it - see resolveNumericOperands) rather than assuming
// int32, so a global initializer using i8/i16/i64/f32/f64 folds to a
// constant of the correct LLVM type instead of a mismatched i32 the module
// verifier would reject.
func (g *Generator) constBinaryExpr(n ast.NodeIndex) (llvm.Value, bool) {
	lNode := g.tree.Child(n, 0)
	rNode := g.tree.Child(n, 1)
	lv, ok1 := g.constExpr(lNode)
	rv, ok2 := g.constExpr(rNode)
	if !ok1 || !ok2 {
		return llvm.Value{}, false
	}
	op := g.tree.Text(n)
	lt := g.info.Types[lNode]

	if lt.Kind == sema.TypeBool {
		lb := lv.ZExtValue() != 0
		rb := rv.ZExtValue() != 0
		switch op {
		case "&&":
			return g.constBool(lb && rb), true
		case "||":
			return g.constBool(lb || rb), true
		case "==":
			return g.constBool(lb == rb), true
		case "!=":
			return g.constBool(lb != rb), true
		default:
			g.errorAt(n, "unsupported constant operator %s for bool", op)
			return llvm.Value{}, false
		}
	}

	if lt.IsFloatKind() {
		return g.constFloatBinaryExpr(n, op, lv, rv)
	}

	if lt.IsUnsigned() {
		return g.constUnsignedBinaryExpr(n, op, lv, rv)
	}

	li := lv.SExtValue()
	ri := rv.SExtValue()
	resultType := g.info.Types[n]
	switch op {
	case "+":
		return g.constIntOfType(resultType, li+ri), true
	case "-":
		return g.constIntOfType(resultType, li-ri), true
	case "*":
		return g.constIntOfType(resultType, li*ri), true
	case "/":
		if ri == 0 {
			g.errorAt(n, "division by zero in constant expression")
			return llvm.Value{}, false
		}
		return g.constIntOfType(resultType, li/ri), true
	case "%":
		if ri == 0 {
			g.errorAt(n, "division by zero in constant expression")
			return llvm.Value{}, false
		}
		return g.constIntOfType(resultType, li%ri), true
	case "&":
		return g.constIntOfType(resultType, li&ri), true
	case "|":
		return g.constIntOfType(resultType, li|ri), true
	case "^":
		return g.constIntOfType(resultType, li^ri), true
	case "==":
		return g.constBool(li == ri), true
	case "!=":
		return g.constBool(li != ri), true
	case "<":
		return g.constBool(li < ri), true
	case "<=":
		return g.constBool(li <= ri), true
	case ">":
		return g.constBool(li > ri), true
	case ">=":
		return g.constBool(li >= ri), true
	default:
		g.errorAt(n, "unsupported constant operator %s", op)
		return llvm.Value{}, false
	}
}

// constUnsignedBinaryExpr is constBinaryExpr's unsigned-integer branch: reads
// both operands as uint64 (ZExtValue, so a high-bit-set u64 constant isn't
// misread as negative) and uses unsigned division/remainder/ordering, where a
// signed fold would give wrong results. Add/sub/mul/bitwise share the signed
// path's semantics (same low bits after constIntOfType truncates to width), so
// they differ from constBinaryExpr only in operand width interpretation here.
func (g *Generator) constUnsignedBinaryExpr(n ast.NodeIndex, op string, lv, rv llvm.Value) (llvm.Value, bool) {
	lu := lv.ZExtValue()
	ru := rv.ZExtValue()
	resultType := g.info.Types[n]
	switch op {
	case "+":
		return g.constIntOfType(resultType, int64(lu+ru)), true
	case "-":
		return g.constIntOfType(resultType, int64(lu-ru)), true
	case "*":
		return g.constIntOfType(resultType, int64(lu*ru)), true
	case "/":
		if ru == 0 {
			g.errorAt(n, "division by zero in constant expression")
			return llvm.Value{}, false
		}
		return g.constIntOfType(resultType, int64(lu/ru)), true
	case "%":
		if ru == 0 {
			g.errorAt(n, "division by zero in constant expression")
			return llvm.Value{}, false
		}
		return g.constIntOfType(resultType, int64(lu%ru)), true
	case "&":
		return g.constIntOfType(resultType, int64(lu&ru)), true
	case "|":
		return g.constIntOfType(resultType, int64(lu|ru)), true
	case "^":
		return g.constIntOfType(resultType, int64(lu^ru)), true
	case "==":
		return g.constBool(lu == ru), true
	case "!=":
		return g.constBool(lu != ru), true
	case "<":
		return g.constBool(lu < ru), true
	case "<=":
		return g.constBool(lu <= ru), true
	case ">":
		return g.constBool(lu > ru), true
	case ">=":
		return g.constBool(lu >= ru), true
	default:
		g.errorAt(n, "unsupported constant operator %s", op)
		return llvm.Value{}, false
	}
}

// constFloatBinaryExpr is constBinaryExpr's float-kind branch (f32/f64).
func (g *Generator) constFloatBinaryExpr(n ast.NodeIndex, op string, lv, rv llvm.Value) (llvm.Value, bool) {
	lf, _ := lv.DoubleValue()
	rf, _ := rv.DoubleValue()
	resultType := g.info.Types[n]
	switch op {
	case "+":
		return llvm.ConstFloat(g.llvmType(resultType), lf+rf), true
	case "-":
		return llvm.ConstFloat(g.llvmType(resultType), lf-rf), true
	case "*":
		return llvm.ConstFloat(g.llvmType(resultType), lf*rf), true
	case "/":
		if rf == 0 {
			g.errorAt(n, "division by zero in constant expression")
			return llvm.Value{}, false
		}
		return llvm.ConstFloat(g.llvmType(resultType), lf/rf), true
	case "==":
		return g.constBool(lf == rf), true
	case "!=":
		return g.constBool(lf != rf), true
	case "<":
		return g.constBool(lf < rf), true
	case "<=":
		return g.constBool(lf <= rf), true
	case ">":
		return g.constBool(lf > rf), true
	case ">=":
		return g.constBool(lf >= rf), true
	default:
		g.errorAt(n, "unsupported constant operator %s for float", op)
		return llvm.Value{}, false
	}
}

// constCompositeLit folds a struct or array composite literal built
// entirely from constant elements. A keyed struct literal that leaves a
// field unmentioned zero-fills it, same as genCompositeLitInto does at
// runtime (see AGENTS.md/sema: a keyed literal need not name every field).
func (g *Generator) constCompositeLit(n ast.NodeIndex) (llvm.Value, bool) {
	t := g.info.Types[n]
	_, elems := g.tree.CompositeLitElems(n)

	switch t.Kind {
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		vals := make([]llvm.Value, len(layout.fieldTypes))
		for i, ft := range layout.fieldTypes {
			vals[i] = llvm.ConstNull(ft)
		}
		keyed := len(elems) > 0 && g.tree.IsKeyedElement(elems[0])
		for i, e := range elems {
			idx, valueNode := g.structLitFieldSlot(layout, e, i, keyed)
			v, ok := g.constExpr(valueNode)
			if !ok {
				return llvm.Value{}, false
			}
			vals[idx] = v
		}
		return llvm.ConstNamedStruct(layout.llvmType, vals), true

	case sema.TypeArray:
		if t.Dynamic {
			// A slice literal always needs a real runtime heap allocation
			// (the arena - see LANGUAGE.md's "Dynamic arrays" section), so
			// it can never be a compile-time constant, unlike a fixed-size
			// array literal (a plain LLVM ConstArray, no allocation needed).
			g.errorAt(n, "global initializer must be a compile-time constant expression")
			return llvm.Value{}, false
		}
		elemType := g.llvmType(*t.Elem)
		vals := make([]llvm.Value, len(elems))
		for i, e := range elems {
			v, ok := g.constExpr(e)
			if !ok {
				return llvm.Value{}, false
			}
			vals[i] = v
		}
		return llvm.ConstArray(elemType, vals), true

	default:
		g.errorAt(n, "%s is not a valid constant composite literal type", t)
		return llvm.Value{}, false
	}
}

// constIntOfType builds a constant integer of type t's LLVM width (i8/i16/
// i32/i64) from v - LLVM's ConstInt already truncates to the target type's
// bit width internally, so this is correct for any width without needing a
// per-width Go-side truncation first.
func (g *Generator) constIntOfType(t sema.Type, v int64) llvm.Value {
	return llvm.ConstInt(g.llvmType(t), uint64(v), true)
}

func (g *Generator) constBool(b bool) llvm.Value {
	var iv uint64
	if b {
		iv = 1
	}
	return llvm.ConstInt(g.boolTy, iv, false)
}

// isConstFoldable reports whether n is structurally shaped like a compile-
// time constant expression - the same node-kind shapes constExpr itself
// knows how to fold (see this file's own doc comment) - without actually
// evaluating anything or emitting any diagnostic. genGlobalVarDecl calls this
// first to decide which of the two lowering paths a global's initializer
// takes (constExpr directly, or the synthesized init function - see
// globalinit.go); once this returns true, constExpr is guaranteed to only
// ever hit one of its genuinely-erroneous cases (division by zero, an
// out-of-range literal, ...), never its "not a constant at all" default
// cases, so a diagnostic constExpr reports past this point is always real.
//
// This intentionally mirrors constExpr/constStringText's own switch shape
// node-kind-for-node-kind, rather than reusing constExpr's evaluation and
// silently discarding whatever diagnostic it produced along the way: the
// latter would either lose a genuine "constant but erroneous" diagnostic
// (dividing a constant by a constant zero) by conflating it with "not
// constant, run it at init time instead" - deferring a division that would
// otherwise be a clean compile-time error into an actual runtime crash - or
// require a much more invasive silence-diagnostics-during-probing mechanism
// threaded through every recursive constExpr helper. A second small,
// clearly-labeled traversal is the simpler, safer trade-off.
func (g *Generator) isConstFoldable(n ast.NodeIndex) bool {
	if t := g.info.Types[n]; t.Kind == sema.TypeString {
		return g.isConstFoldableString(n)
	}

	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.NumberLit, enums.NodeKinds.BoolLit:
		return true
	case enums.NodeKinds.ParenExpr:
		return g.isConstFoldable(g.tree.Child(n, 0))
	case enums.NodeKinds.UnaryExpr:
		// Mirror constUnaryExpr's own operator gate: only "-"/"!" ever fold.
		// "&"/"*" (address-of/deref) are also UnaryExpr, but sema already
		// restricts their operands to shapes (Ident/MemberExpr/IndexExpr/
		// UnaryExpr("*")) this function never marks foldable anyway - this
		// check makes that guarantee local instead of incidental.
		text := g.tree.Text(n)
		return (text == "-" || text == "!") && g.isConstFoldable(g.tree.Child(n, 0))
	case enums.NodeKinds.BinaryExpr:
		return g.isConstFoldable(g.tree.Child(n, 0)) && g.isConstFoldable(g.tree.Child(n, 1))
	case enums.NodeKinds.CompositeLit:
		return g.isConstFoldableCompositeLit(n)
	default:
		return false
	}
}

// isConstFoldableString is isConstFoldable's string-typed counterpart,
// mirroring constStringText's own recursive shape (a StringLit, or
// `+`-concatenation of constant strings, arbitrarily nested/parenthesized).
func (g *Generator) isConstFoldableString(n ast.NodeIndex) bool {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.StringLit:
		return true
	case enums.NodeKinds.ParenExpr:
		return g.isConstFoldableString(g.tree.Child(n, 0))
	case enums.NodeKinds.BinaryExpr:
		return g.tree.Text(n) == "+" &&
			g.isConstFoldableString(g.tree.Child(n, 0)) &&
			g.isConstFoldableString(g.tree.Child(n, 1))
	default:
		return false
	}
}

// isConstFoldableCompositeLit is isConstFoldable's CompositeLit case,
// mirroring constCompositeLit's own shape: a struct or fixed-size array
// literal built entirely from constant elements. A dynamic-array (slice)
// literal is never foldable - it always needs a real runtime heap allocation
// (see LANGUAGE.md's "Dynamic arrays" section), which is exactly the kind of
// initializer the synthesized init function (globalinit.go) now exists to
// handle, rather than a case constExpr needs to reject.
func (g *Generator) isConstFoldableCompositeLit(n ast.NodeIndex) bool {
	t := g.info.Types[n]
	_, elems := g.tree.CompositeLitElems(n)

	switch t.Kind {
	case sema.TypeStruct:
		for _, e := range elems {
			valueNode := e
			if g.tree.IsKeyedElement(e) {
				valueNode = g.tree.Child(e, 1)
			}
			if !g.isConstFoldable(valueNode) {
				return false
			}
		}
		return true

	case sema.TypeArray:
		if t.Dynamic {
			return false
		}
		for _, e := range elems {
			if !g.isConstFoldable(e) {
				return false
			}
		}
		return true

	default:
		return false
	}
}

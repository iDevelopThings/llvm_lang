// This file implements codegen's compile-time constant evaluator, used only
// for top-level `var` initializers (see codegen.go's genGlobalVarDecl and
// AGENTS.md's codegen section for the decision this encodes: a global's
// initializer must already be a compile-time constant - there is no
// synthesized Go-style init-routine-before-main in this language). It is
// deliberately more capable than sema/typecheck.go's constArraySize (which
// only ever needs a bare integer literal for an array size): literals,
// parenthesization, unary +/-/!, binary arithmetic/comparison/logical/string-
// concatenation, and struct/array composite literals built entirely from
// constants. Anything else (a reference to another variable, a function
// call, `this`, a member/index expression) is not a constant expression this
// package can fold, and is reported as a codegen diagnostic.
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

package codegen

import (
	"strconv"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// genAddr computes the address of an lvalue expression - an Ident, `this`,
// a MemberExpr (struct field), an IndexExpr (array element), or (through
// ParenExpr) any of those parenthesized. Anything else - a value expression
// used somewhere an address is needed (a method-call receiver that isn't
// itself addressable, e.g. indexing straight into a call's result) - is
// evaluated as a plain rvalue and spilled into a fresh stack slot, so every
// caller (assignment, ++/--, member/index GEP chains, a method call's
// receiver) can treat "get me an address" uniformly regardless of how the
// underlying expression came to exist.
func (g *Generator) genAddr(n ast.NodeIndex) llvm.Value {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		sym := g.info.Refs[n]
		if addr, ok := g.locals[sym]; ok {
			return addr
		}
		if addr, ok := g.globals[sym]; ok {
			return addr
		}
		panic("codegen: identifier " + sym.Name + " has no storage")

	case enums.NodeKinds.ThisExpr:
		// The receiver is already a pointer parameter (see
		// declareFuncSignature/genFuncBody) - its address *is* its value,
		// no alloca of its own needed.
		return g.curReceiver

	case enums.NodeKinds.MemberExpr:
		objNode := g.tree.Child(n, 0)
		base := g.genAddr(objNode)
		layout := g.structLayouts[g.info.Types[objNode].Struct]
		idx := layout.fieldIndex[g.info.Refs[n]]
		return g.builder.CreateStructGEP(layout.llvmType, base, idx, "")

	case enums.NodeKinds.IndexExpr:
		targetNode := g.tree.Child(n, 0)
		indexNode := g.tree.Child(n, 1)
		base := g.genAddr(targetNode)
		idx := g.genExpr(indexNode)
		targetType := g.info.Types[targetNode]
		g.genBoundsCheck(idx, targetType.Size)
		arrType := g.llvmType(targetType)
		zero := llvm.ConstInt(g.i32Ty, 0, false)
		return g.builder.CreateInBoundsGEP(arrType, base, []llvm.Value{zero, idx}, "")

	case enums.NodeKinds.ParenExpr:
		return g.genAddr(g.tree.Child(n, 0))

	case enums.NodeKinds.CompositeLit:
		t := g.llvmType(g.info.Types[n])
		tmp := g.createEntryAlloca(t, "lit")
		g.genCompositeLitInto(tmp, n)
		return tmp

	default:
		v := g.genExpr(n)
		t := g.llvmType(g.info.Types[n])
		tmp := g.createEntryAlloca(t, "tmp")
		g.builder.CreateStore(v, tmp)
		return tmp
	}
}

// genLoad computes n's address (genAddr) and loads its value - the common
// path shared by every lvalue-shaped expression kind used as an rvalue
// (Ident naming a var/param, MemberExpr, IndexExpr, ThisExpr). Ident's own
// genExpr case doesn't always go through this - a bare reference to a
// declared *function* has no address to load from at all (see genExpr's
// Ident case, and genFuncValue).
func (g *Generator) genLoad(n ast.NodeIndex) llvm.Value {
	addr := g.genAddr(n)
	return g.builder.CreateLoad(g.llvmType(g.info.Types[n]), addr, "")
}

// genFuncValue builds the fat-pointer value {fnPtr, ctxPtr} for a bare,
// uncalled reference to a free function (`add`, not `add(...)`) - see
// CODEGEN.md's "First-class functions" section for the representation this
// implements.
//
// ctxPtr is always a null pointer constant here: every function value this
// round is a free-function reference, so there is no receiver to close
// over. This is the exact extension point a future bound-method value
// (`p.move` referenced without a call) will fill in instead - constructing
// this same two-field struct but with the receiver's own address as ctxPtr
// rather than null - so the representation and calling convention need no
// redesign when that lands.
func (g *Generator) genFuncValue(sym *sema.Symbol) llvm.Value {
	entry := g.funcs[sym.Decl]
	ctxPtr := llvm.ConstNull(g.ptrTy)
	// Must go through g.ctx (not the package-level llvm.ConstStruct), same
	// reasoning as constStringValue: otherwise the result's type is a
	// structurally-identical but distinct anonymous struct type from
	// g.funcValTy, and LLVM's verifier rejects assigning it to anything
	// actually typed g.funcValTy (a local's alloca, a param, a return slot).
	return g.ctx.ConstStruct([]llvm.Value{entry.fn, ctxPtr}, false)
}

// genBoundsCheck emits a real runtime check that idx (an i32) satisfies
// `0 <= idx < size` for a fixed-size array of that size, trapping
// immediately (llvm.trap followed by unreachable - see setupRuntime,
// runtime.go) rather than falling through to an out-of-bounds GEP, which
// would otherwise be silent undefined behavior - a read/write through
// arbitrary memory. See AGENTS.md's "Array bounds checking" section.
//
// Structurally the same CreateCondBr/AddBasicBlock shape genIfStmt/
// genForStmt/genShortCircuit already use elsewhere in this package: a
// condition, a taken ("trap") block, and a not-taken ("continue") block.
// Leaves the builder positioned at the end of the continue block, so a
// caller (genAddr's IndexExpr case) simply keeps emitting the actual
// GEP/load/store right after this call, exactly as if no check had run at
// all.
func (g *Generator) genBoundsCheck(idx llvm.Value, size int64) {
	zero := llvm.ConstInt(g.i32Ty, 0, true)
	sizeConst := llvm.ConstInt(g.i32Ty, uint64(size), true)
	geZero := g.builder.CreateICmp(llvm.IntSGE, idx, zero, "")
	ltSize := g.builder.CreateICmp(llvm.IntSLT, idx, sizeConst, "")
	inBounds := g.builder.CreateAnd(geZero, ltSize, "")

	trapBB := g.ctx.AddBasicBlock(g.curFn, "idx.trap")
	okBB := g.ctx.AddBasicBlock(g.curFn, "idx.ok")
	g.builder.CreateCondBr(inBounds, okBB, trapBB)

	g.builder.SetInsertPointAtEnd(trapBB)
	g.builder.CreateCall(g.trapType, g.trapFn, nil, "")
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(okBB)
}

// genExpr lowers n to its rvalue.
func (g *Generator) genExpr(n ast.NodeIndex) llvm.Value {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		// A bare, uncalled reference to a declared free function (`add`,
		// not `add(...)`) has no storage location to load from - genAddr
		// would panic on it (see its own Ident case) - so it's built
		// directly as a fat-pointer value instead. Every other Ident (a
		// var/param) still goes through the ordinary addr+load path.
		if sym := g.info.Refs[n]; sym.Kind == sema.SymFunc {
			return g.genFuncValue(sym)
		}
		return g.genLoad(n)
	case enums.NodeKinds.MemberExpr, enums.NodeKinds.IndexExpr, enums.NodeKinds.ThisExpr:
		return g.genLoad(n)
	case enums.NodeKinds.NumberLit:
		return g.genNumberLit(n)
	case enums.NodeKinds.StringLit:
		return g.constStringValue(g.tree.File.StringValue(g.tree.Nodes[n].Tok))
	case enums.NodeKinds.BoolLit:
		return g.genBoolLit(n)
	case enums.NodeKinds.ParenExpr:
		return g.genExpr(g.tree.Child(n, 0))
	case enums.NodeKinds.UnaryExpr:
		return g.genUnaryExpr(n)
	case enums.NodeKinds.BinaryExpr:
		return g.genBinaryExpr(n)
	case enums.NodeKinds.CallExpr:
		return g.genCallExpr(n)
	case enums.NodeKinds.CompositeLit:
		t := g.llvmType(g.info.Types[n])
		tmp := g.createEntryAlloca(t, "lit")
		g.genCompositeLitInto(tmp, n)
		return g.builder.CreateLoad(t, tmp, "")
	default:
		panic("codegen: cannot generate an expression of kind " + g.tree.Nodes[n].Kind.String())
	}
}

// genNumberLit lowers n to its already-resolved concrete numeric constant.
// sema's untyped-constant resolution (see AGENTS.md's Types section) always
// pins info.Types[n] to one of the six concrete numeric kinds by the time a
// checked tree reaches codegen - never one of the two untyped bookkeeping
// kinds - so this only ever needs to pick the matching integer/float
// constructor for that kind's width.
func (g *Generator) genNumberLit(n ast.NodeIndex) llvm.Value {
	t := g.info.Types[n]
	text := g.tree.Text(n)
	switch t.Kind {
	case sema.TypeI8:
		return g.genIntLit(n, text, g.i8Ty, 8)
	case sema.TypeI16:
		return g.genIntLit(n, text, g.i16Ty, 16)
	case sema.TypeI32:
		return g.genIntLit(n, text, g.i32Ty, 32)
	case sema.TypeI64:
		return g.genIntLit(n, text, g.i64Ty, 64)
	case sema.TypeF32:
		return g.genFloatLit(text, g.f32Ty)
	case sema.TypeF64:
		return g.genFloatLit(text, g.f64Ty)
	default:
		panic("codegen: genNumberLit reached a non-numeric type " + t.String())
	}
}

// genIntLit parses text (n's literal text, always plain decimal digits - the
// lexer's exponent/decimal-point syntax means a *float*-kind literal, handled
// by genFloatLit instead) into a bits-wide signed integer constant. An
// out-of-range literal for that width still gets a codegen diagnostic rather
// than silently wrapping, since sema itself never checks literal range.
func (g *Generator) genIntLit(n ast.NodeIndex, text string, llt llvm.Type, bits int) llvm.Value {
	v, err := strconv.ParseInt(text, 10, bits)
	if err != nil {
		g.errorAt(n, "integer literal %q is out of range for a %d-bit int", text, bits)
		return llvm.ConstInt(llt, 0, true)
	}
	return llvm.ConstInt(llt, uint64(v), true)
}

// genFloatLit parses text (a decimal-point/exponent literal) into a
// constant of the given float type (f32 or f64) - ConstFloat takes a Go
// float64 regardless of the target LLVM type's own width, truncating as
// needed for f32.
func (g *Generator) genFloatLit(text string, llt llvm.Type) llvm.Value {
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		// sema already validated this is a real numeric literal - reaching
		// a parse failure here would mean the lexer accepted something
		// strconv can't parse, which shouldn't happen on a checked tree.
		return llvm.ConstFloat(llt, 0)
	}
	return llvm.ConstFloat(llt, v)
}

func (g *Generator) genBoolLit(n ast.NodeIndex) llvm.Value {
	return g.constBool(g.tree.Nodes[n].Tok.Keyword == enums.Keywords.True)
}

func (g *Generator) genUnaryExpr(n ast.NodeIndex) llvm.Value {
	operand := g.tree.Child(n, 0)
	v := g.genExpr(operand)
	switch g.tree.Text(n) {
	case "-":
		if g.info.Types[operand].IsFloatKind() {
			return g.builder.CreateFNeg(v, "")
		}
		return g.builder.CreateNeg(v, "")
	case "!":
		return g.builder.CreateNot(v, "")
	default:
		panic("codegen: unsupported unary operator " + g.tree.Text(n))
	}
}

// genBinaryExpr lowers every binary operator AGENTS.md's Operators section
// documents. `&&`/`||` short-circuit via genShortCircuit (their right
// operand must not evaluate when the left already decides the result -
// important once it can have side effects, e.g. a call); every other
// operator evaluates both operands eagerly. A numeric operator dispatches to
// the matching float instruction (CreateFAdd/CreateFCmp/...) whenever the
// left operand's already-resolved concrete type (sema guarantees both
// operands share one - see AGENTS.md's Types section) is a float kind;
// otherwise the existing integer instructions apply directly, unchanged
// across every int width (an LLVM integer instruction is generic over bit
// width as long as both operands share the same LLVM type, which sema
// already guarantees here).
func (g *Generator) genBinaryExpr(n ast.NodeIndex) llvm.Value {
	op := g.tree.Text(n)
	lNode := g.tree.Child(n, 0)
	rNode := g.tree.Child(n, 1)

	if op == "&&" || op == "||" {
		return g.genShortCircuit(op, lNode, rNode)
	}

	lv := g.genExpr(lNode)
	rv := g.genExpr(rNode)
	lt := g.info.Types[lNode]
	isFloat := lt.IsFloatKind()

	switch op {
	case "+":
		switch {
		case lt.Kind == sema.TypeString:
			return g.genStringConcat(lv, rv)
		case isFloat:
			return g.builder.CreateFAdd(lv, rv, "")
		default:
			return g.builder.CreateAdd(lv, rv, "")
		}
	case "-":
		if isFloat {
			return g.builder.CreateFSub(lv, rv, "")
		}
		return g.builder.CreateSub(lv, rv, "")
	case "*":
		if isFloat {
			return g.builder.CreateFMul(lv, rv, "")
		}
		return g.builder.CreateMul(lv, rv, "")
	case "/":
		if isFloat {
			return g.builder.CreateFDiv(lv, rv, "")
		}
		return g.builder.CreateSDiv(lv, rv, "")
	case "%":
		return g.builder.CreateSRem(lv, rv, "")
	case "&":
		return g.builder.CreateAnd(lv, rv, "")
	case "|":
		return g.builder.CreateOr(lv, rv, "")
	case "^":
		return g.builder.CreateXor(lv, rv, "")
	case "==":
		switch {
		case lt.Kind == sema.TypeString:
			return g.genStringEqual(lv, rv, true)
		case lt.Kind == sema.TypeStruct, lt.Kind == sema.TypeArray:
			return g.genValueEqual(lt, lv, rv)
		case isFloat:
			return g.builder.CreateFCmp(llvm.FloatOEQ, lv, rv, "")
		default:
			return g.builder.CreateICmp(llvm.IntEQ, lv, rv, "")
		}
	case "!=":
		switch {
		case lt.Kind == sema.TypeString:
			return g.genStringEqual(lv, rv, false)
		case lt.Kind == sema.TypeStruct, lt.Kind == sema.TypeArray:
			return g.builder.CreateNot(g.genValueEqual(lt, lv, rv), "")
		case isFloat:
			return g.builder.CreateFCmp(llvm.FloatUNE, lv, rv, "")
		default:
			return g.builder.CreateICmp(llvm.IntNE, lv, rv, "")
		}
	case "<", "<=", ">", ">=":
		if lt.Kind == sema.TypeString {
			return g.genStringOrder(op, lv, rv)
		}
		if isFloat {
			return g.genFloatOrder(op, lv, rv)
		}
		return g.genIntOrder(op, lv, rv)
	default:
		panic("codegen: unsupported binary operator " + op)
	}
}

// genIntOrder lowers `< <= > >=` for two already-evaluated, same-type signed
// integer operands (any width - see AGENTS.md's Operators section).
func (g *Generator) genIntOrder(op string, lv, rv llvm.Value) llvm.Value {
	switch op {
	case "<":
		return g.builder.CreateICmp(llvm.IntSLT, lv, rv, "")
	case "<=":
		return g.builder.CreateICmp(llvm.IntSLE, lv, rv, "")
	case ">":
		return g.builder.CreateICmp(llvm.IntSGT, lv, rv, "")
	default:
		return g.builder.CreateICmp(llvm.IntSGE, lv, rv, "")
	}
}

// genFloatOrder lowers `< <= > >=` for two already-evaluated, same-type
// float operands (f32 or f64). Uses the *ordered* FCmp predicates (OLT/OLE/
// OGT/OGE) - false whenever either operand is NaN, matching Go's own float
// comparison semantics (see AGENTS.md's Operators section).
func (g *Generator) genFloatOrder(op string, lv, rv llvm.Value) llvm.Value {
	switch op {
	case "<":
		return g.builder.CreateFCmp(llvm.FloatOLT, lv, rv, "")
	case "<=":
		return g.builder.CreateFCmp(llvm.FloatOLE, lv, rv, "")
	case ">":
		return g.builder.CreateFCmp(llvm.FloatOGT, lv, rv, "")
	default:
		return g.builder.CreateFCmp(llvm.FloatOGE, lv, rv, "")
	}
}

// genValueEqual reports whether two already-evaluated struct or array values
// (or, recursively, any field/element type reachable from one - int, string,
// bool, a nested struct, or a nested array) are equal, per Go's own rule: a
// struct equals another of the same type iff every field does, recursively;
// an array equals another iff every element does, recursively (see
// AGENTS.md's Operators section). Always returns "are these equal" - never
// pre-negated (unlike genStringEqual) - a `!=` caller just Nots the result
// once (see genBinaryExpr).
//
// Every field/element access here goes through ExtractValue on the already-
// loaded aggregate value, mirroring genPrintStructValue/genPrintArrayValue
// (runtime.go) rather than genAddr's GEP-based addressing: lv/rv are rvalues
// already sitting in SSA registers (structs/arrays are real LLVM aggregate
// values in this package - see AGENTS.md), not memory locations, so there is
// no address to compute in the first place.
//
// This builds one full comparison that ANDs every field/element's result
// together, rather than short-circuiting on the first unequal one - simpler
// codegen (no extra basic blocks per field/element to wire up), and every
// operand here is a pure value comparison with nothing side-effecting to
// avoid re-evaluating. Short-circuiting would be a reasonable alternative
// (and likely produces tighter codegen for a large early-differing
// aggregate) but isn't required for correctness, so the simpler shape was
// chosen - see AGENTS.md.
func (g *Generator) genValueEqual(t sema.Type, lv, rv llvm.Value) llvm.Value {
	switch t.Kind {
	case sema.TypeInt, sema.TypeBool:
		return g.builder.CreateICmp(llvm.IntEQ, lv, rv, "")
	case sema.TypeString:
		return g.genStringEqual(lv, rv, true)
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		result := llvm.ConstInt(g.boolTy, 1, false)
		for i, ft := range layout.fieldSemaTypes {
			lf := g.builder.CreateExtractValue(lv, i, "")
			rf := g.builder.CreateExtractValue(rv, i, "")
			result = g.builder.CreateAnd(result, g.genValueEqual(ft, lf, rf), "")
		}
		return result
	case sema.TypeArray:
		result := llvm.ConstInt(g.boolTy, 1, false)
		for i := 0; i < int(t.Size); i++ {
			le := g.builder.CreateExtractValue(lv, i, "")
			re := g.builder.CreateExtractValue(rv, i, "")
			result = g.builder.CreateAnd(result, g.genValueEqual(*t.Elem, le, re), "")
		}
		return result
	default:
		// Only int/string/bool/struct/array Types exist (see sema/types.go),
		// and every one is handled above - unreachable on a tree that
		// already passed sema.Check (see the package doc comment).
		panic("codegen: genValueEqual reached an unsupported type " + t.String())
	}
}

// genShortCircuit lowers `&&`/`||` with real short-circuit control flow (not
// an eager bitwise AND/OR): the right operand's basic block is only reached
// when the left operand hasn't already decided the result.
func (g *Generator) genShortCircuit(op string, lNode, rNode ast.NodeIndex) llvm.Value {
	lv := g.genExpr(lNode)
	startBB := g.builder.GetInsertBlock()

	rhsBB := g.ctx.AddBasicBlock(g.curFn, "sc.rhs")
	contBB := g.ctx.AddBasicBlock(g.curFn, "sc.cont")
	if op == "&&" {
		g.builder.CreateCondBr(lv, rhsBB, contBB)
	} else {
		g.builder.CreateCondBr(lv, contBB, rhsBB)
	}

	g.builder.SetInsertPointAtEnd(rhsBB)
	rv := g.genExpr(rNode)
	rhsEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	phi := g.builder.CreatePHI(g.boolTy, "")
	shortVal := llvm.ConstInt(g.boolTy, 0, false)
	if op == "||" {
		shortVal = llvm.ConstInt(g.boolTy, 1, false)
	}
	phi.AddIncoming([]llvm.Value{shortVal, rv}, []llvm.BasicBlock{startBB, rhsEndBB})
	return phi
}

// genCallExpr lowers a call: the predeclared print builtin (see
// isPrintCall), an explicit numeric conversion `T(x)` (see isConversionCall),
// a free function, a method call, or an indirect call through a
// function-typed value (see isDirectFuncCall/genIndirectCall). print returns
// no value (void) - see AGENTS.md's "print builtin" section - so its
// "result" is never used by anything else in a checked tree.
func (g *Generator) genCallExpr(n ast.NodeIndex) llvm.Value {
	children := g.tree.Children(n)
	calleeNode, argNodes := children[0], children[1:]

	if g.isPrintCall(calleeNode) {
		g.genPrintCall(argNodes[0])
		return llvm.Value{}
	}
	if g.isConversionCall(calleeNode) {
		return g.genConversion(n, argNodes[0])
	}
	switch {
	case g.tree.Nodes[calleeNode].Kind == enums.NodeKinds.MemberExpr:
		return g.genMethodCall(calleeNode, argNodes)
	case g.isDirectFuncCall(calleeNode):
		return g.genFuncCall(calleeNode, argNodes)
	default:
		return g.genIndirectCall(calleeNode, argNodes)
	}
}

// isDirectFuncCall mirrors sema's own dispatch (funcSigForCall in
// sema/typecheck.go): a call's callee compiles to a plain, direct `call`
// instruction with zero fat-pointer/indirect-call overhead only when it's a
// plain Ident resolving (via Info.Refs) to an actual declared free function
// (SymFunc with a real FuncDecl, i.e. Decl != InvalidNode) - the
// predeclared `print` builtin is intercepted earlier by isPrintCall and
// never reaches here on a checked tree, but Decl != InvalidNode still
// guards against it defensively. Anything else that type-checked as
// callable - a function-typed variable/parameter, or any other expression
// whose value is itself a function (e.g. a call result) - goes through
// genIndirectCall instead (see CODEGEN.md's "First-class functions" section).
func (g *Generator) isDirectFuncCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymFunc && sym.Decl != ast.InvalidNode
}

// genIndirectCall lowers a call through a function-typed value - a
// variable/parameter holding a function reference, or any other expression
// whose value is itself a function (e.g. one returned from another call).
// Unlike a direct call (genFuncCall, which looks the callee's LLVM function
// straight up in g.funcs), this actually evaluates calleeNode as an
// ordinary value expression to get its fat-pointer {fnPtr, ctxPtr}
// representation (see genFuncValue and CODEGEN.md), extracts fnPtr, and builds
// the llvm.FunctionType to call through it directly from the callee's own
// sema.Type (Params/Return) - there's no FuncDecl node backing an indirect
// callee the way genFuncCall's funcEntry lookup relies on. ctxPtr is
// unused this round - every function value is a free-function reference
// (see genFuncValue) - so it's never passed along as a hidden argument.
func (g *Generator) genIndirectCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	fnVal := g.genExpr(calleeNode)
	fnPtr := g.builder.CreateExtractValue(fnVal, 0, "")

	calleeType := g.info.Types[calleeNode]
	paramTypes := make([]llvm.Type, len(calleeType.Params))
	for i, pt := range calleeType.Params {
		paramTypes[i] = g.llvmType(pt)
	}
	fnType := llvm.FunctionType(g.llvmType(*calleeType.Return), paramTypes, false)

	args := make([]llvm.Value, len(argNodes))
	for i, a := range argNodes {
		args[i] = g.genExpr(a)
	}
	return g.builder.CreateCall(fnType, fnPtr, args, "")
}

// isConversionCall mirrors sema's own recognition of `T(x)` as an explicit
// numeric conversion rather than an ordinary call (see
// sema/typecheck.go's checkConversionCall): the callee is a plain Ident
// whose Info.Refs resolution is a type symbol, not a function. On a tree
// that already passed sema.Check, this can only ever be SymBuiltinType - a
// non-numeric conversion target (SymStruct) would already have been
// rejected by sema and so can never reach codegen at all (see the package
// doc comment).
func (g *Generator) isConversionCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymBuiltinType
}

// genConversion lowers a validated numeric conversion `T(x)`: sema has
// already confirmed both the argument's and the call's own (target) types
// are numeric and recorded the target as the CallExpr node's own Type in
// info.Types (checkConversionCall), so this reads both ends straight out of
// info.Types with no re-derivation of its own. A same-Kind "conversion"
// (`i32(someI32Value)`) passes the value through unchanged rather than
// emitting a pointless instruction; otherwise the correct LLVM conversion
// instruction is chosen for the source/target pair - sign-extend for a
// wider integer, truncate for a narrower one (every integer here is signed -
// see AGENTS.md's Types section), int-to-float/float-to-int via
// CreateSIToFP/CreateFPToSI, and extend/truncate between float widths via
// CreateFPExt/CreateFPTrunc.
func (g *Generator) genConversion(n, argNode ast.NodeIndex) llvm.Value {
	from := g.info.Types[argNode]
	to := g.info.Types[n]
	v := g.genExpr(argNode)

	if from.Kind == to.Kind {
		return v
	}
	toLLT := g.llvmType(to)

	switch {
	case from.IsIntegerKind() && to.IsIntegerKind():
		if to.Bits() > from.Bits() {
			return g.builder.CreateSExt(v, toLLT, "")
		}
		return g.builder.CreateTrunc(v, toLLT, "")
	case from.IsIntegerKind() && to.IsFloatKind():
		return g.builder.CreateSIToFP(v, toLLT, "")
	case from.IsFloatKind() && to.IsIntegerKind():
		return g.builder.CreateFPToSI(v, toLLT, "")
	case from.IsFloatKind() && to.IsFloatKind():
		if to.Bits() > from.Bits() {
			return g.builder.CreateFPExt(v, toLLT, "")
		}
		return g.builder.CreateFPTrunc(v, toLLT, "")
	default:
		panic("codegen: genConversion reached a non-numeric type pair on a checked tree")
	}
}

func (g *Generator) genFuncCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	sym := g.info.Refs[calleeNode]
	entry := g.funcs[sym.Decl]
	args := make([]llvm.Value, len(argNodes))
	for i, a := range argNodes {
		args[i] = g.genExpr(a)
	}
	return g.builder.CreateCall(entry.fnType, entry.fn, args, "")
}

// genMethodCall lowers `p.move(...)`: the receiver's address (not its
// loaded value - see AGENTS.md, every method is implicitly by-reference)
// becomes the call's hidden first argument.
func (g *Generator) genMethodCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	objNode := g.tree.Child(calleeNode, 0)
	sym := g.info.Refs[calleeNode]
	entry := g.funcs[sym.Decl]

	args := make([]llvm.Value, len(argNodes)+1)
	args[0] = g.genAddr(objNode)
	for i, a := range argNodes {
		args[i+1] = g.genExpr(a)
	}
	return g.builder.CreateCall(entry.fnType, entry.fn, args, "")
}

// genCompositeLitInto fills dst (an already-addressed struct or array
// destination) field-by-field/element-by-element, rather than building a
// temporary aggregate and copying it - the same "fill the known destination
// directly" approach storeValueInto uses for a var-decl/assignment.
//
// A struct literal zero-fills every field first: a positional literal
// always supplies exactly one value per field (sema requires it), but a
// keyed literal may leave some unmentioned (see AGENTS.md/sema - it's not
// required to name every field), and those need a real zero value, not
// whatever garbage the destination's memory already held.
func (g *Generator) genCompositeLitInto(dst llvm.Value, n ast.NodeIndex) {
	t := g.info.Types[n]
	elems := g.tree.Children(n)[1:]

	switch t.Kind {
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		for i, ft := range layout.fieldTypes {
			addr := g.builder.CreateStructGEP(layout.llvmType, dst, i, "")
			g.builder.CreateStore(llvm.ConstNull(ft), addr)
		}
		keyed := len(elems) > 0 && g.tree.Nodes[elems[0]].Kind == enums.NodeKinds.KeyValueExpr
		if keyed {
			for _, e := range elems {
				sym := g.info.Refs[g.tree.Child(e, 0)]
				addr := g.builder.CreateStructGEP(layout.llvmType, dst, layout.fieldIndex[sym], "")
				g.storeValueInto(addr, g.tree.Child(e, 1))
			}
		} else {
			for i, e := range elems {
				addr := g.builder.CreateStructGEP(layout.llvmType, dst, i, "")
				g.storeValueInto(addr, e)
			}
		}

	case sema.TypeArray:
		arrType := g.llvmType(t)
		zero := llvm.ConstInt(g.i32Ty, 0, false)
		for i, e := range elems {
			idx := llvm.ConstInt(g.i32Ty, uint64(i), false)
			addr := g.builder.CreateInBoundsGEP(arrType, dst, []llvm.Value{zero, idx}, "")
			g.storeValueInto(addr, e)
		}
	}
}

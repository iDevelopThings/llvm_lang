package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// genBlock lowers every statement of block in order, stopping as soon as one
// of them terminates its basic block (a return/break/continue, or an if
// whose every branch terminates) - anything after that point is genuinely
// unreachable (this language has no goto/labels to ever jump back into it),
// so it's simply not generated, rather than built into a dead block. Reports
// whether the block as a whole terminated, so an enclosing if/for knows
// whether to branch to what follows it.
func (g *Generator) genBlock(block ast.NodeIndex) bool {
	for _, stmt := range g.tree.Children(block) {
		if g.genStmt(stmt) {
			return true
		}
	}
	return false
}

// genStmt lowers one statement, reporting whether it terminated the current
// basic block. See ast.Node's doc comment: IfStmt's then/else and ForStmt's
// init/post slots may hold any single statement, not just a Block, so this
// same dispatch (in particular its Block case, recursing into genBlock)
// handles both a nested block and a bare single statement uniformly.
func (g *Generator) genStmt(n ast.NodeIndex) bool {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.VarDecl:
		g.genVarDecl(n)
		return false
	case enums.NodeKinds.ShortVarDecl:
		g.genShortVarDecl(n)
		return false
	case enums.NodeKinds.AssignStmt:
		g.genAssignStmt(n)
		return false
	case enums.NodeKinds.IncDecStmt:
		g.genIncDecStmt(n)
		return false
	case enums.NodeKinds.ExprStmt:
		g.genExpr(g.tree.Child(n, 0))
		return false
	case enums.NodeKinds.DeleteStmt:
		g.genDeleteStmt(n)
		return false
	case enums.NodeKinds.ReturnStmt:
		return g.genReturnStmt(n)
	case enums.NodeKinds.BreakStmt:
		return g.genBreakStmt(n)
	case enums.NodeKinds.ContinueStmt:
		return g.genContinueStmt(n)
	case enums.NodeKinds.Block:
		return g.genBlock(n)
	case enums.NodeKinds.IfStmt:
		return g.genIfStmt(n)
	case enums.NodeKinds.ForStmt:
		return g.genForStmt(n)
	default:
		return false
	}
}

// genVarDecl lowers `var name Type`, `var name = expr`, or
// `var name Type = expr` as a local: a stack slot (see createEntryAlloca),
// zero-initialized when there's no initializer (matching Go's own
// zero-value default), or filled from the initializer otherwise.
func (g *Generator) genVarDecl(n ast.NodeIndex) {
	nameNode := g.tree.Child(n, 0)
	initNode := g.tree.Child(n, 2)
	sym := g.info.Refs[nameNode]

	llt := g.llvmType(g.info.Types[n])
	addr := g.allocLocalSlot(sym, llt, sym.Name)
	g.locals[sym] = addr

	if initNode == ast.InvalidNode {
		g.builder.CreateStore(llvm.ConstNull(llt), addr)
		return
	}
	g.storeValueInto(addr, initNode)
}

// genShortVarDecl lowers `name := expr` - always has an initializer (the
// parser requires one), so unlike genVarDecl there's no zero-init case.
func (g *Generator) genShortVarDecl(n ast.NodeIndex) {
	nameNode := g.tree.Child(n, 0)
	initNode := g.tree.Child(n, 1)
	sym := g.info.Refs[nameNode]

	llt := g.llvmType(g.info.Types[n])
	addr := g.allocLocalSlot(sym, llt, sym.Name)
	g.locals[sym] = addr
	g.storeValueInto(addr, initNode)
}

// storeValueInto stores valueNode's value into the already-computed address
// addr. A composite literal gets filled directly into addr (see
// genCompositeLitInto) rather than built as a temporary and copied - the
// same avoid-the-extra-copy approach used for a var-decl/assignment/
// composite-literal-field destination alike, anywhere a destination address
// is already known up front.
func (g *Generator) storeValueInto(addr llvm.Value, valueNode ast.NodeIndex) {
	if g.tree.Nodes[valueNode].Kind == enums.NodeKinds.CompositeLit {
		g.genCompositeLitInto(addr, valueNode)
		return
	}
	g.builder.CreateStore(g.genExpr(valueNode), addr)
}

// genDeleteStmt lowers `delete p` (see LANGUAGE.md's "Pointers" section): a
// direct call to libc's `free` against p's own pointer value - the real,
// separate heap `new` mallocs from (runtime.go's setupRuntime), never the
// bump-allocator arena, which has no per-allocation free at all.
//
// After the free itself, if the deleted operand is a bare local variable/
// parameter reference (deleteLocalSlot below), its own stack slot is also
// stored-over with a null pointer - a narrow, partial use-after-free
// mitigation: it turns *some* immediate reuse-through-the-same-variable bugs
// (`delete p; *p = 5`) into a clean, deterministic null-pointer-dereference
// trap instead of silently corrupting whatever memory got reallocated into
// that freed slot. This is intentionally the only case handled - see
// deleteLocalSlot's own doc comment and LANGUAGE.md's "Pointers" section for
// exactly what this does and doesn't cover (a struct field, an array/slice
// element, a second variable/parameter holding a copy of the same address,
// and a captured-by-reference outer local are all real, deliberately
// unmitigated use-after-free surfaces still - nulling one variable's own
// slot can never reach any of those).
func (g *Generator) genDeleteStmt(n ast.NodeIndex) {
	operand := g.tree.Child(n, 0)
	ptr := g.genExpr(operand)
	g.builder.CreateCall(g.freeType, g.freeFn, []llvm.Value{ptr}, "")

	if addr, ok := g.deleteLocalSlot(operand); ok {
		g.builder.CreateStore(llvm.ConstNull(g.ptrTy), addr)
	}
}

// deleteLocalSlot reports whether operand (delete's own operand expression,
// stripped of any enclosing parentheses) is a bare reference to a local
// variable/parameter declared directly in the function currently being
// generated - the one case where "the pointer's own storage slot" is
// unambiguous and directly addressable - and if so, returns that slot's
// address (the exact same alloca genAddr's own Ident case would resolve to).
//
// Deliberately narrow: an Ident resolving to a *global* (g.globals, not
// g.locals) or to an outer function's captured symbol (reached through a
// lambda's own closure context, not a plain alloca at all - see
// addrOfSymbol) does not count either, on top of the non-Ident shapes this
// naturally excludes already (a MemberExpr/`.field`, an IndexExpr/`[i]`, or
// any other expression) - only a real, direct alloca in the current
// function's own g.locals is ever nulled.
func (g *Generator) deleteLocalSlot(operand ast.NodeIndex) (llvm.Value, bool) {
	for g.tree.Nodes[operand].Kind == enums.NodeKinds.ParenExpr {
		operand = g.tree.Child(operand, 0)
	}
	if g.tree.Nodes[operand].Kind != enums.NodeKinds.Ident {
		return llvm.Value{}, false
	}
	addr, ok := g.locals[g.info.Refs[operand]]
	return addr, ok
}

// genAssignStmt lowers `=` and the compound forms `+= -= *= /=`. `+=` also
// accepts string (concatenation), matching `+`'s own overload; the rest are
// numeric (any int width or float width - see AGENTS.md's Operators
// section), dispatching to the matching float instruction whenever the
// target's type is a float kind.
func (g *Generator) genAssignStmt(n ast.NodeIndex) {
	targetNode := g.tree.Child(n, 0)
	valueNode := g.tree.Child(n, 1)
	addr := g.genAddr(targetNode)

	op := g.tree.Text(n)
	if op == "=" {
		g.storeValueInto(addr, valueNode)
		return
	}

	targetType := g.info.Types[targetNode]
	cur := g.builder.CreateLoad(g.llvmType(targetType), addr, "")
	rhs := g.genExpr(valueNode)
	isFloat := targetType.IsFloatKind()

	var result llvm.Value
	switch op {
	case "+=", "-=", "*=", "/=":
		baseOp := op[:1] // "+=" -> "+", etc.
		if baseOp == "+" && targetType.Kind == sema.TypeString {
			result = g.genStringConcat(cur, rhs)
		} else {
			result = g.genArithOp(baseOp, cur, rhs, isFloat)
		}
	default:
		panic("codegen: unsupported compound assignment operator " + op)
	}
	g.builder.CreateStore(result, addr)
}

// genIncDecStmt lowers `++`/`--` - any numeric type (any int width or float
// width - see AGENTS.md's Operators section), using the target's own actual
// type/width rather than assuming i32.
func (g *Generator) genIncDecStmt(n ast.NodeIndex) {
	target := g.tree.Child(n, 0)
	addr := g.genAddr(target)
	t := g.info.Types[target]
	llt := g.llvmType(t)
	cur := g.builder.CreateLoad(llt, addr, "")
	isInc := g.tree.Text(n) == "++"

	isFloat := t.IsFloatKind()
	var one llvm.Value
	if isFloat {
		one = llvm.ConstFloat(llt, 1)
	} else {
		one = llvm.ConstInt(llt, 1, true)
	}
	op := "-"
	if isInc {
		op = "+"
	}
	result := g.genArithOp(op, cur, one, isFloat)
	g.builder.CreateStore(result, addr)
}

// genReturnStmt lowers `return` (bare or with a value). A bare return in a
// function declaring no return type - main included - produces `ret void`,
// except main itself always needs a real i32 exit code (see
// declareFuncSignature): a bare `return` in main is `ret i32 0`.
func (g *Generator) genReturnStmt(n ast.NodeIndex) bool {
	valueNode := g.tree.Child(n, 0)
	if valueNode == ast.InvalidNode {
		if g.curFunc.isMain {
			g.builder.CreateRet(llvm.ConstInt(g.i32Ty, 0, false))
		} else {
			g.builder.CreateRetVoid()
		}
		return true
	}
	g.builder.CreateRet(g.genExpr(valueNode))
	return true
}

// genBreakStmt/genContinueStmt branch to the innermost enclosing loop's
// break/continue target (see genForStmt). `sema.Check` now guarantees a
// break/continue only ever appears inside a ForStmt's body (see
// checkBreakOrContinue in sema/typecheck.go, and BLOCKERS.md's codegen-phase
// entry #6 for the gap this closed) - this was previously one of the few
// checks codegen performed on its own (reported as a diagnostic, lowered to
// `unreachable`), back when that guarantee didn't exist yet. An empty
// loopStack here now means the tree wasn't actually valid per sema, which
// this whole package already assumes never happens (see the package doc
// comment) - so, same as everywhere else that assumption is relied on, this
// is a panic rather than a diagnostic.
func (g *Generator) genBreakStmt(n ast.NodeIndex) bool {
	if len(g.loopStack) == 0 {
		panic("codegen: break outside a loop - sema.Check should have rejected this")
	}
	g.builder.CreateBr(g.loopStack[len(g.loopStack)-1].breakTarget)
	return true
}

func (g *Generator) genContinueStmt(n ast.NodeIndex) bool {
	if len(g.loopStack) == 0 {
		panic("codegen: continue outside a loop - sema.Check should have rejected this")
	}
	g.builder.CreateBr(g.loopStack[len(g.loopStack)-1].continueTarget)
	return true
}

// genIfStmt lowers both grammar forms (`if cond: stmt` and the brace form
// with an optional else/else-if chain) - they produce the identical
// [cond, then, else] shape post-parse (see ast.Node's IfStmt doc comment),
// so a single lowering handles both. Reports termination only when both
// branches exist and both terminate; when there's no else, control can
// always still fall through to what follows, so the statement as a whole
// never terminates.
func (g *Generator) genIfStmt(n ast.NodeIndex) bool {
	condNode := g.tree.Child(n, 0)
	thenNode := g.tree.Child(n, 1)
	elseNode := g.tree.Child(n, 2)
	hasElse := elseNode != ast.InvalidNode

	condVal := g.genExpr(condNode)

	thenBB := g.ctx.AddBasicBlock(g.curFn, "if.then")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "if.merge")
	elseBB := mergeBB
	if hasElse {
		elseBB = g.ctx.AddBasicBlock(g.curFn, "if.else")
	}
	g.builder.CreateCondBr(condVal, thenBB, elseBB)

	g.builder.SetInsertPointAtEnd(thenBB)
	thenTerm := g.genStmt(thenNode)
	if !thenTerm {
		g.builder.CreateBr(mergeBB)
	}

	elseTerm := false
	if hasElse {
		g.builder.SetInsertPointAtEnd(elseBB)
		elseTerm = g.genStmt(elseNode)
		if !elseTerm {
			g.builder.CreateBr(mergeBB)
		}
	}

	g.builder.SetInsertPointAtEnd(mergeBB)
	return hasElse && thenTerm && elseTerm
}

// genForStmt lowers all three Go-style for-loop forms uniformly - bare
// `for {}`, cond-only `for cond {}`, and the full `for init; cond; post {}` -
// since ForStmt's [init, cond, post, body] shape already represents every
// form the same way, with the unused clauses simply InvalidNode (see
// ast.Node's ForStmt doc comment).
//
// continue branches to the post-statement block (so post always runs before
// the condition is re-checked, same as Go); break branches past the whole
// loop. A for-loop is conservatively reported as never terminating the
// statement it's part of (control can always reach for.end, at least
// structurally) - detecting a true infinite `for {}` with no reachable
// break is exactly the kind of full flow analysis this project's
// type-checking phase already deferred (see AGENTS.md/BLOCKERS.md), and
// isn't reattempted here.
func (g *Generator) genForStmt(n ast.NodeIndex) bool {
	initNode := g.tree.Child(n, 0)
	condNode := g.tree.Child(n, 1)
	postNode := g.tree.Child(n, 2)
	bodyNode := g.tree.Child(n, 3)

	if initNode != ast.InvalidNode {
		g.genStmt(initNode)
	}

	condBB := g.ctx.AddBasicBlock(g.curFn, "for.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "for.body")
	postBB := g.ctx.AddBasicBlock(g.curFn, "for.post")
	endBB := g.ctx.AddBasicBlock(g.curFn, "for.end")

	g.builder.CreateBr(condBB)
	g.builder.SetInsertPointAtEnd(condBB)
	if condNode != ast.InvalidNode {
		g.builder.CreateCondBr(g.genExpr(condNode), bodyBB, endBB)
	} else {
		g.builder.CreateBr(bodyBB)
	}

	g.builder.SetInsertPointAtEnd(bodyBB)
	g.loopStack = append(g.loopStack, loopCtx{
		breakTarget:    endBB,
		continueTarget: postBB,
	})
	bodyTerm := g.genBlock(bodyNode)
	g.loopStack = g.loopStack[:len(g.loopStack)-1]
	if !bodyTerm {
		g.builder.CreateBr(postBB)
	}

	g.builder.SetInsertPointAtEnd(postBB)
	if postNode != ast.InvalidNode {
		g.genStmt(postNode)
	}
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	return false
}

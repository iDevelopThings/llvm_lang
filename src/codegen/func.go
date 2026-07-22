package codegen

import (
	"fmt"

	"llvm_lang/src/ast"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// declareFuncSignature declares decl's (a FuncDecl's - free function or
// method alike, see ast.Node's doc comment: a method is just a FuncDecl with
// a non-empty receiver child) LLVM function signature, with no body yet -
// split from genFuncBody into its own pass (see genFile) so a call to a
// function declared later in the source, or a recursive/mutually-recursive
// call, always finds its callee already in g.funcs.
//
// A method's implicit receiver (see AGENTS.md: "every method is implicitly
// by-reference") becomes a real, explicit first parameter of pointer-to-
// struct type; there's no separate by-value/by-reference receiver kind to
// distinguish. `main` is special-cased to the real i32-returning LLVM entry
// point signature (see genFuncBody's fallback-terminator logic for the other
// half of that decision).
func (g *Generator) declareFuncSignature(decl ast.NodeIndex) {
	receiver := g.tree.FuncReceiver(decl)
	nameNode := g.tree.FuncName(decl)
	paramListNode := g.tree.FuncParamList(decl)
	returnTypeNode := g.tree.FuncReturnType(decl)
	sym := g.info.Refs[nameNode]

	var paramTypes []llvm.Type
	if receiver != ast.InvalidNode {
		structInfo := g.info.Structs[g.tree.Text(receiver)]
		paramTypes = append(paramTypes, llvm.PointerType(g.structLayouts[structInfo].llvmType, 0))
	}
	for _, paramNode := range g.tree.Children(paramListNode) {
		paramTypes = append(paramTypes, g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)]))
	}

	retType := sema.Type{Kind: sema.TypeVoid}
	if returnTypeNode != ast.InvalidNode {
		retType = g.info.Types[returnTypeNode]
	}

	isMain := receiver == ast.InvalidNode && g.tree.Text(nameNode) == "main"
	llvmRet := g.llvmType(retType)
	name := g.tree.Text(nameNode)
	switch {
	case isMain:
		// main's declared return type is already validated by sema
		// (checkMainReturnType, src/sema/typecheck.go: either nothing or
		// int) - main's real LLVM signature is always i32-returning
		// regardless, since it must hand a real exit code back to the OS
		// caller even when the source declares no return type at all (see
		// CODEGEN.md's "main is the real entry point" section).
		llvmRet = g.i32Ty
		name = "main"
	case receiver != ast.InvalidNode:
		name = g.tree.Text(receiver) + "." + name
	}

	fnType := llvm.FunctionType(llvmRet, paramTypes, false)
	g.funcs[sym] = funcEntry{
		fn:       llvm.AddFunction(g.mod, name, fnType),
		fnType:   fnType,
		retType:  retType,
		isMethod: receiver != ast.InvalidNode,
	}
}

// genFuncBody lowers decl's body, given its signature already declared (see
// declareFuncSignature). Every VarDecl/ShortVarDecl/Param in the body gets a
// stack slot via createEntryAlloca; a method's receiver needs none of its
// own - the incoming pointer parameter already *is* its address (see
// genAddr's ThisExpr case).
func (g *Generator) genFuncBody(decl ast.NodeIndex) {
	receiver := g.tree.FuncReceiver(decl)
	nameNode := g.tree.FuncName(decl)
	paramListNode := g.tree.FuncParamList(decl)
	returnTypeNode := g.tree.FuncReturnType(decl)
	body := g.tree.FuncBody(decl)

	entry := g.funcs[g.info.Refs[nameNode]]
	g.curFn = entry.fn
	g.entryBlock = g.ctx.AddBasicBlock(g.curFn, "entry")
	g.builder.SetInsertPointAtEnd(g.entryBlock)
	g.locals = make(map[*sema.Symbol]llvm.Value)
	g.loopStack = nil

	offset := 0
	g.curReceiver = llvm.Value{}
	if receiver != ast.InvalidNode {
		g.curReceiver = g.curFn.Param(0)
		offset = 1
	}
	for i, paramNode := range g.tree.Children(paramListNode) {
		psym := g.info.Refs[g.tree.Child(paramNode, 0)]
		pllt := g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)])
		addr := g.createEntryAlloca(pllt, psym.Name)
		g.builder.CreateStore(g.curFn.Param(offset+i), addr)
		g.locals[psym] = addr
	}

	g.curFunc = &funcCtx{
		isMain:    receiver == ast.InvalidNode && g.tree.Text(nameNode) == "main",
		hasReturn: returnTypeNode != ast.InvalidNode,
	}

	if !g.genBlock(body) {
		g.emitFallbackTerminator()
	}
	g.curFunc = nil
}

// emitFallbackTerminator runs whenever a function's lowered body falls off
// the end without every path already ending in a terminator instruction -
// LLVM requires every basic block to end in exactly one.
//
// `sema.Check` now runs a full "does every path return" flow analysis of its
// own (isTerminatingStmt in sema/typecheck.go, mirroring Go's own spec's
// "terminating statements" - see AGENTS.md's "Missing return" section) and
// rejects any function declaring a return type whose body isn't guaranteed
// to return on every path. So, on a tree that already passed sema.Check, a
// non-void, non-main function should never actually reach this fallback at
// all - but this is left in place anyway, deliberately, as a defensive
// backstop: it costs nothing at runtime (it only ever fires once per
// function, at codegen time), and it guards against any gap in the flow
// analysis itself (this package's own doc comment already assumes its input
// passed sema.Check; if that assumption were ever wrong, `unreachable` is a
// far better failure mode than an invalid, terminator-less basic block that
// would otherwise fail LLVM's verifier with a much less specific error).
//   - `main`, and any function declaring no return type, get a real,
//     correct terminator (`ret i32 0` / `ret void`) - falling off the end of
//     a void function is legitimate Go-like behavior, not a bug (sema places
//     no termination requirement on it either), and main must always return
//     a real exit code to its OS caller, never UB.
//   - any other non-void function gets `unreachable` - reaching this given
//     the above should be impossible on a validated tree; `unreachable`
//     documents that assumption directly in the IR rather than inventing a
//     fake return value that could silently mask a real bug.
func (g *Generator) emitFallbackTerminator() {
	switch {
	case g.curFunc.isMain:
		g.builder.CreateRet(llvm.ConstInt(g.i32Ty, 0, false))
	case !g.curFunc.hasReturn:
		g.builder.CreateRetVoid()
	default:
		g.builder.CreateUnreachable()
	}
}

// declareConstructorSignature declares ctor's (a ConstructorDecl's) LLVM
// function signature, with no body yet - split from genConstructorBody for
// the same reason declareFuncSignature is split from genFuncBody: a
// constructor call appearing anywhere else in the whole program (another
// constructor, an ordinary function body, a struct in a different file or
// package) must always find its callee already declared, regardless of
// declaration order (see LANGUAGE.md's "Constructors" section).
//
// A constructor reuses the exact same implicit-first-pointer-parameter
// convention an ordinary method's receiver already uses (see
// declareFuncSignature above and CODEGEN.md's "Method receivers" section) -
// the struct being constructed, addressed, not loaded - followed by its own
// declared parameters, and always returns void: a constructor never
// declares (or needs) a return type of its own, since it "returns" the
// struct implicitly by populating `this` (see genConstructorCall, which
// does the actual by-value handoff to the call site, exactly like a
// composite literal already does).
//
// Each constructor's generated LLVM function is named
// "Struct.constructor.N" (N its declared parameter count) - the same
// "Type.MethodName" convention declareFuncSignature already uses for an
// ordinary method, adapted for a constructor's lack of a name of its own:
// arity is the one thing that already uniquely identifies a struct's
// constructor (see StructInfo.Constructors), so it doubles as the
// disambiguating suffix here too.
func (g *Generator) declareConstructorSignature(ctor ast.NodeIndex) {
	sym := g.info.Refs[ctor]
	structInfo := sym.StructInfo
	layout := g.structLayouts[structInfo]

	paramListNode := g.tree.ConstructorParamList(ctor)
	paramNodes := g.tree.Children(paramListNode)

	paramTypes := make([]llvm.Type, 0, len(paramNodes)+1)
	paramTypes = append(paramTypes, llvm.PointerType(layout.llvmType, 0))
	for _, paramNode := range paramNodes {
		paramTypes = append(paramTypes, g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)]))
	}

	fnType := llvm.FunctionType(g.voidTy, paramTypes, false)
	name := fmt.Sprintf("%s.constructor.%d", structInfo.Symbol.Name, len(paramNodes))
	g.ctors[sym] = funcEntry{
		fn:       llvm.AddFunction(g.mod, name, fnType),
		fnType:   fnType,
		retType:  sema.Type{Kind: sema.TypeVoid},
		isMethod: true,
	}
}

// genConstructorBody lowers ctor's body, given its signature already
// declared (see declareConstructorSignature) - mirrors genFuncBody almost
// exactly, except a constructor's receiver parameter is always present
// (param 0, unconditionally - a constructor without an implicit `this`
// wouldn't be a constructor) and it never declares a return type, so
// emitFallbackTerminator's "no declared return type" branch (`ret void`) is
// always the right fallback for one, the same as an ordinary void
// function/method.
func (g *Generator) genConstructorBody(ctor ast.NodeIndex) {
	sym := g.info.Refs[ctor]
	entry := g.ctors[sym]
	paramListNode := g.tree.ConstructorParamList(ctor)
	body := g.tree.ConstructorBody(ctor)

	g.curFn = entry.fn
	g.entryBlock = g.ctx.AddBasicBlock(g.curFn, "entry")
	g.builder.SetInsertPointAtEnd(g.entryBlock)
	g.locals = make(map[*sema.Symbol]llvm.Value)
	g.loopStack = nil
	g.curReceiver = g.curFn.Param(0)

	for i, paramNode := range g.tree.Children(paramListNode) {
		psym := g.info.Refs[g.tree.Child(paramNode, 0)]
		pllt := g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)])
		addr := g.createEntryAlloca(pllt, psym.Name)
		g.builder.CreateStore(g.curFn.Param(1+i), addr)
		g.locals[psym] = addr
	}

	g.curFunc = &funcCtx{
		isMain:    false,
		hasReturn: false,
	}

	if !g.genBlock(body) {
		g.emitFallbackTerminator()
	}
	g.curFunc = nil
}

// createEntryAlloca allocates a stack slot of type t in the current
// function's entry block, regardless of where the builder is currently
// inserting - every local var/param this package generates goes through
// this, not a plain CreateAlloca at the point of declaration, specifically
// so a var-decl inside a loop body allocates once (in the entry block) and
// is simply re-stored each iteration, rather than growing the stack by one
// alloca per iteration (a non-entry-block alloca is a genuinely fresh stack
// slot on every dynamic execution, not just a lexical one).
func (g *Generator) createEntryAlloca(t llvm.Type, name string) llvm.Value {
	savedBB := g.builder.GetInsertBlock()
	if first := g.entryBlock.FirstInstruction(); first.IsNil() {
		g.builder.SetInsertPointAtEnd(g.entryBlock)
	} else {
		g.builder.SetInsertPointBefore(first)
	}
	addr := g.builder.CreateAlloca(t, name)
	g.builder.SetInsertPointAtEnd(savedBB)
	return addr
}

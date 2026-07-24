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
		// The receiver names either a declared struct or a declared enum
		// (see LANGUAGE.md's "Enums" section: methods reuse the exact same
		// receiver-clause syntax, needing zero parser grammar changes) - an
		// enum receiver's own implicit pointer parameter always points at
		// the one shared g.enumValTy, exactly like a pointer's own pointee
		// type never affects its LLVM representation elsewhere in this
		// package.
		receiverName := g.tree.Text(receiver)
		if structInfo, ok := g.info.Structs[receiverName]; ok {
			paramTypes = append(paramTypes, llvm.PointerType(g.structLayouts[structInfo].llvmType, 0))
		} else {
			paramTypes = append(paramTypes, llvm.PointerType(g.enumValTy, 0))
		}
	}
	for _, paramNode := range g.tree.Children(paramListNode) {
		paramTypes = append(paramTypes, g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)]))
	}

	retType := sema.Type{Kind: sema.TypeVoid}
	if returnTypeNode != ast.InvalidNode {
		retType = g.info.Types[returnTypeNode]
	}

	// A generator function (`yield T` return type - see LANGUAGE.md's
	// "Generator functions" section) gets one implicit trailing parameter
	// beyond its declared ones - the consumer's own synthesized callback fat
	// pointer (the exact same {fnPtr, ctxPtr} representation this language's
	// first-class functions/lambdas already use - see funcValTy) - and its
	// real LLVM return type is always void, regardless of its declared
	// element type: every `yield expr` inside its body lowers to an indirect
	// call through this trailing parameter instead of a real return value
	// (see genYieldStmt, stmt.go).
	isGenerator := retType.Kind == sema.TypeGenerator
	var llvmRet llvm.Type
	if isGenerator {
		paramTypes = append(paramTypes, g.funcValTy)
		llvmRet = g.voidTy
		retType = sema.Type{Kind: sema.TypeVoid}
	} else {
		llvmRet = g.llvmType(retType)
	}

	isMain := receiver == ast.InvalidNode && g.tree.Text(nameNode) == "main"
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

// declareExternFuncSignature declares decl's (an ExternFuncDecl's) LLVM
// function signature - the FFI counterpart to declareFuncSignature above,
// binding a real external C symbol rather than lowering a function this
// package itself generates a body for (see LANGUAGE.md's "External functions
// (FFI)" section and ast.Node's own ExternFuncDecl doc comment: there is no
// body at all, ever, so - unlike every other declare*Signature in this
// package - genPackage below never follows this with a corresponding
// "generate body" pass for it).
//
// Simpler than declareFuncSignature in exactly the ways an extern func's own
// grammar already guarantees: no receiver (an extern func can never be a
// method - there's no grammar for one, see parser.parseExternFuncDecl) and no
// `main`-name special-casing (main is always a real, bodied FuncDecl,
// trivially never an ExternFuncDecl at all). Declared with default linkage,
// not private - exactly like printf/malloc/memcpy/memcmp in runtime.go -
// since this name must resolve as a genuine external symbol: the JIT's
// already-registered process-symbol generator (see cmd/llvmc/main.go's
// bindMinGWMainThunk - no changes needed here at all) resolves it against
// whatever real DLL export already loaded into the host process happens to
// share this exact name, at JIT-execution time.
//
// Stores into the exact same g.funcs map declareFuncSignature does, keyed by
// the identical sym (both declare a SymFunc symbol - see resolve.go's
// declareExternFunc) - every call-site (genFuncCall, isDirectFuncCall,
// genFuncValue/genFuncThunk) reads this map with zero awareness of which of
// the two declare*Signature functions actually populated a given entry.
func (g *Generator) declareExternFuncSignature(decl ast.NodeIndex) {
	nameNode := g.tree.ExternFuncName(decl)
	paramListNode := g.tree.ExternFuncParamList(decl)
	returnTypeNode := g.tree.ExternFuncReturnType(decl)
	sym := g.info.Refs[nameNode]

	var paramTypes []llvm.Type
	for _, paramNode := range g.tree.Children(paramListNode) {
		paramTypes = append(paramTypes, g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)]))
	}

	retType := sema.Type{Kind: sema.TypeVoid}
	if returnTypeNode != ast.InvalidNode {
		retType = g.info.Types[returnTypeNode]
	}

	fnType := llvm.FunctionType(g.llvmType(retType), paramTypes, false)
	g.funcs[sym] = funcEntry{
		fn:       llvm.AddFunction(g.mod, g.tree.Text(nameNode), fnType),
		fnType:   fnType,
		retType:  retType,
		isMethod: false,
	}
}

// beginSyntheticFunc resets Generator's per-function generation fields
// (curFn/entryBlock/locals/loopStack/matchExprStack/destructors, plus
// curReceiver/curCtxPtr/curCaptureIndex/curCaptureTy and
// curIsGenerator/curGeneratorCallback/curGeneratorElem back to "no receiver,
// no lambda capture context, not a generator") to lower fn's own body from
// scratch, leaving fn's own fresh entry block as the builder's current insert
// point - the reset shape genFuncBody/genConstructorBody/genDestructorBody/
// buildGlobalInitFn (globalinit.go)/buildArgsInitFn (args.go)/genLambdaFunc
// (expr.go)/genRangeGeneratorCallbackFunc (stmt.go) all need, deduplicated
// here rather than hand-copies that could drift.
//
// Returns a restore func undoing this. Every call site but genLambdaFunc can
// ignore it - an ordinary top-level body never depends on what the previous
// one left behind. Only genLambdaFunc's own body generation nests *inside*
// another function's still-in-progress generation, so it alone calls
// restore once its literal's body is done (curFunc and the builder's saved
// insert block are genLambdaFunc's own to save/restore alongside this, since
// each is set to a real, call-site-specific value right after this returns).
func (g *Generator) beginSyntheticFunc(fn llvm.Value) (restore func()) {
	savedFn, savedEntry, savedLocals := g.curFn, g.entryBlock, g.locals
	savedLoopStack, savedDestructors := g.loopStack, g.destructors
	savedMatchExprStack := g.matchExprStack
	savedReceiver := g.curReceiver
	savedCtxPtr, savedCaptureIndex, savedCaptureTy := g.curCtxPtr, g.curCaptureIndex, g.curCaptureTy
	savedIsGenerator, savedGeneratorCallback, savedGeneratorElem := g.curIsGenerator, g.curGeneratorCallback, g.curGeneratorElem

	g.curFn = fn
	g.entryBlock = g.ctx.AddBasicBlock(fn, "entry")
	g.builder.SetInsertPointAtEnd(g.entryBlock)
	g.locals = make(map[*sema.Symbol]llvm.Value)
	g.loopStack = nil
	g.matchExprStack = nil
	g.destructors = nil
	g.curReceiver = llvm.Value{}
	g.curCtxPtr = llvm.Value{}
	g.curCaptureIndex = nil
	g.curCaptureTy = llvm.Type{}
	g.curIsGenerator = false
	g.curGeneratorCallback = llvm.Value{}
	g.curGeneratorElem = sema.Type{}

	return func() {
		g.curFn, g.entryBlock, g.locals = savedFn, savedEntry, savedLocals
		g.loopStack, g.destructors = savedLoopStack, savedDestructors
		g.matchExprStack = savedMatchExprStack
		g.curReceiver = savedReceiver
		g.curCtxPtr, g.curCaptureIndex, g.curCaptureTy = savedCtxPtr, savedCaptureIndex, savedCaptureTy
		g.curIsGenerator, g.curGeneratorCallback, g.curGeneratorElem = savedIsGenerator, savedGeneratorCallback, savedGeneratorElem
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
	paramNodes := g.tree.Children(paramListNode)

	declaredRetType := sema.Type{Kind: sema.TypeVoid}
	if returnTypeNode != ast.InvalidNode {
		declaredRetType = g.info.Types[returnTypeNode]
	}
	isGenerator := declaredRetType.Kind == sema.TypeGenerator

	entry := g.funcs[g.info.Refs[nameNode]]
	g.beginSyntheticFunc(entry.fn)

	offset := 0
	if receiver != ast.InvalidNode {
		g.curReceiver = g.curFn.Param(0)
		offset = 1
	}
	for i, paramNode := range paramNodes {
		psym := g.info.Refs[g.tree.Child(paramNode, 0)]
		ptype := g.info.Types[g.tree.Child(paramNode, 1)]
		addr := g.allocLocalSlot(psym, g.llvmType(ptype), psym.Name)
		g.builder.CreateStore(g.curFn.Param(offset+i), addr)
		g.locals[psym] = addr
		g.pushDestructorEntry(psym, ptype)
	}

	retType := declaredRetType
	if isGenerator {
		// The generator's own implicit trailing callback parameter (see
		// declareFuncSignature) - always the real function's very last
		// parameter, after every declared one.
		g.curIsGenerator = true
		g.curGeneratorCallback = g.curFn.Param(offset + len(paramNodes))
		g.curGeneratorElem = *declaredRetType.Elem
		retType = sema.Type{Kind: sema.TypeVoid}
	}

	g.curFunc = &funcCtx{
		isMain:    receiver == ast.InvalidNode && g.tree.Text(nameNode) == "main",
		hasReturn: returnTypeNode != ast.InvalidNode && !isGenerator,
		retType:   retType,
	}

	g.finishBody(body)
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

// finishBody generates body - a function/constructor/destructor/lambda's own
// top-level block - and, only if it fell off its own end normally rather
// than already ending in a real `return` somewhere inside it, unwinds
// whatever's left on Generator.destructors before falling back to
// emitFallbackTerminator: an explicit return already unwinds every entry
// itself (see genReturnStmt, stmt.go, and LANGUAGE.md's "Destructors"
// section), but genBlock's own fall-through case only ever unwinds body's
// own directly-declared locals (see unwindDestructorsTo, stmt.go) - so
// whatever's left here is exactly this function/constructor/destructor's own
// by-value parameters, pushed by pushDestructorEntry before body ever started
// generating.
func (g *Generator) finishBody(body ast.NodeIndex) {
	if !g.genBlock(body) {
		g.unwindDestructorsTo(0)
		g.emitFallbackTerminator()
	}
}

// pushDestructorEntry records sym (a local var/short-var-decl/parameter
// declaration whose storage - g.locals[sym] - was just initialized) onto the
// current function's destructor stack if, and only if, its own declared
// type t is a struct or enum that declares its own destructor() directly -
// see LANGUAGE.md's "Destructors" section: a type that's merely non-copyable
// via a field/variant never cascades into an automatic call by itself, only
// a type's own destructor ever does.
func (g *Generator) pushDestructorEntry(sym *sema.Symbol, t sema.Type) {
	entry, ok := g.destructorFuncFor(t)
	if !ok {
		return
	}
	g.destructors = append(g.destructors, destructorEntry{
		sym:  sym,
		fn:   entry.fn,
		fnTy: entry.fnType,
	})
}

// destructorFuncFor reports the destructor function to call for a value of
// type t - a struct's or enum's own declareDestructorSignature/
// declareEnumDestructorSignature entry - and whether t actually has one at
// all (false for any other type, or a struct/enum declaring no destructor of
// its own). Shared by pushDestructorEntry above and destructorFuncForPointee
// (stmt.go, delete's own identical question about a pointer's pointee type).
func (g *Generator) destructorFuncFor(t sema.Type) (funcEntry, bool) {
	switch t.Kind {
	case sema.TypeStruct:
		if t.Struct == nil || t.Struct.Destructor == nil {
			return funcEntry{}, false
		}
		return g.dtors[t.Struct], true
	case sema.TypeEnum:
		if t.Enum == nil || t.Enum.Destructor == nil {
			return funcEntry{}, false
		}
		return g.enumDtors[t.Enum], true
	default:
		return funcEntry{}, false
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

	g.beginSyntheticFunc(entry.fn)
	g.curReceiver = g.curFn.Param(0)

	for i, paramNode := range g.tree.Children(paramListNode) {
		psym := g.info.Refs[g.tree.Child(paramNode, 0)]
		ptype := g.info.Types[g.tree.Child(paramNode, 1)]
		addr := g.allocLocalSlot(psym, g.llvmType(ptype), psym.Name)
		g.builder.CreateStore(g.curFn.Param(1+i), addr)
		g.locals[psym] = addr
		g.pushDestructorEntry(psym, ptype)
	}

	g.curFunc = &funcCtx{
		isMain:    false,
		hasReturn: false,
		retType:   sema.Type{Kind: sema.TypeVoid},
	}

	g.finishBody(body)
	g.curFunc = nil
}

// declareDestructorSignature declares dtor's (a DestructorDecl's) LLVM
// function signature, with no body yet - the destructor-kind counterpart to
// declareConstructorSignature, mirroring it closely: the same implicit-
// first-pointer-parameter convention (the struct instance being destructed,
// addressed, never loaded), no parameters of its own (sema.checkDestructorDecl
// already guarantees an empty paramList), and always void, since a
// destructor is never called with call-expression syntax at all (see
// LANGUAGE.md's "Destructors" section) - only ever invoked implicitly, by
// this package itself, at a local's scope exit or by `delete`.
//
// Named "Struct.destructor" (no arity suffix needed - unlike a constructor,
// a struct declares at most one), the same "Type.MethodName" convention
// declareConstructorSignature already uses.
func (g *Generator) declareDestructorSignature(dtor ast.NodeIndex) {
	sym := g.info.Refs[dtor]
	structInfo := sym.StructInfo
	layout := g.structLayouts[structInfo]

	paramTypes := []llvm.Type{llvm.PointerType(layout.llvmType, 0)}
	fnType := llvm.FunctionType(g.voidTy, paramTypes, false)
	name := fmt.Sprintf("%s.destructor", structInfo.Symbol.Name)
	g.dtors[structInfo] = funcEntry{
		fn:       llvm.AddFunction(g.mod, name, fnType),
		fnType:   fnType,
		retType:  sema.Type{Kind: sema.TypeVoid},
		isMethod: true,
	}
}

// genDestructorBody lowers dtor's body, given its signature already declared
// (see declareDestructorSignature) - mirrors genConstructorBody, minus the
// parameter loop (a destructor never has any of its own).
func (g *Generator) genDestructorBody(dtor ast.NodeIndex) {
	sym := g.info.Refs[dtor]
	entry := g.dtors[sym.StructInfo]
	body := g.tree.DestructorBody(dtor)

	g.beginSyntheticFunc(entry.fn)
	g.curReceiver = g.curFn.Param(0)

	g.curFunc = &funcCtx{
		isMain:    false,
		hasReturn: false,
		retType:   sema.Type{Kind: sema.TypeVoid},
	}

	g.finishBody(body)
	g.curFunc = nil
}

// allocLocalSlot returns the storage address to use for sym - a var/
// short-var-decl/parameter declaration's own Symbol - deciding between a real
// stack alloca (createEntryAlloca, unchanged default) and an arena-heap
// allocation (genArenaAlloc, see CODEGEN.md's "Lambdas" section) based on
// sym.Captured, sema's own capture-analysis verdict (see sema/capture.go):
// a variable/parameter some FuncLit anywhere captures by reference needs
// storage that can safely outlive this function's own stack frame - that
// lambda's value may be returned, stored, or passed onward, well past the
// point this function itself returns - which a stack address cannot survive,
// but the arena's process-lifetime allocation always does (a real,
// intentional leak, exactly consistent with this project's already-
// documented arena philosophy - see BLOCKERS.md). Both paths return the
// identical `ptr`-typed llvm.Value shape (this project already uses LLVM's
// opaque pointers everywhere - see codegen.go's ptrTy field comment), so
// every caller (genVarDecl, genShortVarDecl, genFuncBody/
// genConstructorBody/genLambdaFunc's own param loops) treats the result
// exactly the same regardless of which one it turned out to be - loaded from
// and stored to exactly like any other local's address.
func (g *Generator) allocLocalSlot(sym *sema.Symbol, t llvm.Type, name string) llvm.Value {
	if sym.Captured {
		return g.genArenaAlloc(llvm.SizeOf(t))
	}
	return g.createEntryAlloca(t, name)
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

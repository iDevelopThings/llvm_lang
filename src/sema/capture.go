// Capture analysis for function-literal expressions (FuncLit - see
// LANGUAGE.md's "Lambdas" section): for every lambda anywhere in a tree,
// determine which enclosing local variables/parameters it reads or writes
// by reference, so codegen knows which locals need arena (heap) storage
// instead of an ordinary stack alloca (see CODEGEN.md), and so it can build
// each literal's own capture-context struct layout (Info.Captures).
package sema

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
)

// computeCaptures runs capture analysis for every FuncLit in tree, using
// info's already-fully-resolved Refs/Scopes (see resolve.go's resolvePackage,
// the only caller, which runs this after every file's ordinary lexical
// resolution is done). Iterates every node in the tree's own flat array
// rather than walking from the root, since a FuncLit's own position in the
// tree doesn't matter for this pass - only its own subtree does (see
// analyzeFuncLitCaptures) - and this way nothing needs its own recursive
// tree-walker just to *find* every literal first.
func computeCaptures(tree *ast.Tree, info *Info, bag *diag.Bag) {
	for idx := 1; idx < len(tree.Nodes); idx++ {
		lit := ast.NodeIndex(idx)
		if tree.Nodes[lit].Kind != enums.NodeKinds.FuncLit {
			continue
		}
		analyzeFuncLitCaptures(tree, info, bag, lit)
	}
}

// analyzeFuncLitCaptures computes lit's own free-variable set: every
// Ident/ThisExpr reference anywhere in lit's subtree - deliberately including
// inside any FuncLit nested inside lit, not stopping at its boundary; see the
// paragraph below for why that's exactly what's needed - that resolves (via
// info.Refs) to a Symbol declared in a strictly enclosing function scope
// (isProperAncestorFuncScope), as opposed to lit's own params/locals or
// anything declared inside a lambda nested inside it.
//
// This is a deliberately simple, conservative rule, not a real escape
// analysis (see AGENTS.md's Architecture section on preferring correctness
// over an optimization not asked for): every captured Symbol gets marked
// Symbol.Captured = true unconditionally, and every one of them is recorded
// into info.Captures[lit] in first-seen order (stable, since that order
// becomes the literal's own capture-context struct's field order at codegen
// time) - there's no attempt to prove a particular lambda could never
// actually escape its declaring function's stack frame and so could have kept
// a plain stack alloca.
//
// Walking lit's *entire* subtree - through a nested FuncLit's own body too,
// not just lit's immediate statements - is what makes a variable captured
// two or more function levels down work with no special relaying logic
// anywhere: say `outer` declares `x`, `outer` directly declares lambda1
// (`func(){ ... }`), and lambda1 itself declares lambda2, which is the one
// that actually references `x`. That reference is still a descendant node of
// lambda1 (lambda2's own FuncLit node lives inside lambda1's body), so
// lambda1's own walk finds it too, and - since `outer`'s scope is just as
// much a strict ancestor of lambda1's own function scope as it is of
// lambda2's - `x` is marked captured for *lambda1* as well, even though
// lambda1's own statements never mention `x` directly. That's exactly the
// address codegen needs lambda1 to have on hand (in its own capture context)
// to be able to relay it into lambda2's capture context when lambda2 is
// constructed from inside lambda1's own generated function body (see
// CODEGEN.md's "Lambdas" section) - every FuncLit is analyzed this same way,
// completely independently, so the relay falls out for free with no
// additional bookkeeping in this pass at all.
func analyzeFuncLitCaptures(tree *ast.Tree, info *Info, bag *diag.Bag, lit ast.NodeIndex) {
	fnScope := info.Scopes[lit]
	if fnScope == nil {
		return // defensive - resolveFuncLit always creates one for a FuncLit
	}

	seen := make(map[*Symbol]bool)
	var captures []*Symbol

	walkCaptureRefs(tree, lit, func(refNode ast.NodeIndex) {
		sym, ok := info.Refs[refNode]
		if !ok {
			return
		}
		switch sym.Kind {
		case SymVar, SymParam:
			owner := nearestFunc(sym.Scope)
			if owner == nil || !isProperAncestorFuncScope(owner, fnScope) {
				return // sym.Scope isn't a strictly enclosing function - not a capture
			}
			if seen[sym] {
				return
			}
			seen[sym] = true
			sym.Captured = true
			captures = append(captures, sym)
		case SymReceiver:
			// this's own Symbol.Scope IS its owning method's function scope
			// directly (see resolveFuncBody/resolveConstructorBody), unlike a
			// var/param's (which is whatever block/func scope actually
			// declares it) - no nearestFunc indirection needed here.
			if isProperAncestorFuncScope(sym.Scope, fnScope) {
				span := tree.SpanOf(refNode)
				bag.ErrorfSpan(span.Start, span.End, "cannot capture `this` inside a function literal")
			}
		}
	})

	if len(captures) > 0 {
		info.Captures[lit] = captures
	}
}

// isProperAncestorFuncScope reports whether candidate is a strict ancestor of
// of - found by walking of.Parent upward, never considering of itself - the
// "declared in a genuinely enclosing function, not this literal's own (or one
// nested inside it)" test analyzeFuncLitCaptures needs. A symbol declared
// inside a FuncLit nested *inside* lit fails this the same way a symbol
// declared directly inside lit itself does: its own owning function scope is
// a descendant of fnScope, not an ancestor, so walking upward from fnScope
// never reaches it - exactly the distinction that keeps a nested lambda's own
// purely-local variables from being mistaken for something lit itself needs
// to capture.
func isProperAncestorFuncScope(candidate, of *Scope) bool {
	for s := of.Parent; s != nil; s = s.Parent {
		if s == candidate {
			return true
		}
	}
	return false
}

// walkCaptureRefs calls visit for every Ident/ThisExpr node in n's subtree
// (n included), depth-first - see analyzeFuncLitCaptures' own doc comment for
// why deliberately walking straight through a nested FuncLit's own subtree,
// rather than stopping at its boundary, is exactly what makes multi-level
// capture relaying work with no special-casing. A declaration occurrence (a
// param's own name, a var's own name) is visited exactly like a referencing
// one - both are plain Ident nodes with an info.Refs entry - but always
// resolves to a Symbol whose own Scope is fnScope itself or one of its
// descendants, which isProperAncestorFuncScope already excludes, so no
// separate declaration/reference distinction is needed here at all.
func walkCaptureRefs(tree *ast.Tree, n ast.NodeIndex, visit func(ast.NodeIndex)) {
	if n == ast.InvalidNode {
		return
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident, enums.NodeKinds.ThisExpr:
		visit(n)
	}
	for _, c := range tree.Children(n) {
		walkCaptureRefs(tree, c, visit)
	}
}

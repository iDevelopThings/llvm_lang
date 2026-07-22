# Blockers

Genuine open questions hit while building llvm_lang that need a human
judgment call and aren't reasonably inferable from AGENTS.md or this
codebase's established patterns. This file tracks *unanswered questions*,
not a changelog or a TODO list - once a question gets an answer (whether
from the user directly or as an unambiguous default), the entry should be
removed, not just annotated "resolved" and left to accumulate. The actual
decision belongs in AGENTS.md (the language's own spec, which is durable);
a resolved engineering discovery worth remembering belongs as a code
comment at the site it matters. This file should usually be short or
empty - a long file here means either a lot is genuinely undecided right
now, or old entries didn't get cleaned up after being answered.

Each entry: what the question is, why it couldn't be inferred, and (while
still open) whatever reasonable default is being used in the meantime so
work isn't blocked on an answer.

---

## Real memory-management strategy

The arena allocator (`CODEGEN.md`'s "The arena allocator" section) is a
real, intentional, permanent leak - one process-lifetime bump allocator,
no per-allocation `free`, no refcounting, no GC. It was built as
groundwork/a centralized allocation point, not as an answer to whether this
language needs a real memory strategy eventually (scoped stack-frame frees
when a value provably can't escape, refcounting, or a tracing GC are all
still on the table, each with very different implications for the
language's semantics and runtime complexity). Not inferable from
established patterns - this is a genuine design fork only the user can make
the call on. **Current default while open:** keep leaking via the arena;
every new arena-shaped heap-needing feature (e.g. dynamic arrays) routes
through the same `arena_alloc` primitive rather than inventing its own
allocation path, so there's still only one call site to change once this is
answered.

Pointers' own `new`/`delete` (`LANGUAGE.md`'s "Pointers" section) are a
real, separate, already-answered exception to this specific question - a
plain individually-`malloc`'d/`free`'d block per `new`, deliberately never
routed through the arena (see `DECISIONS.md`) - not a general answer to *this*
entry: they don't help a program that leaks via string concatenation or
dynamic-array growth in a loop, and destructors/RAII (automatic cleanup at
`delete` time) remain unimplemented and out of scope. The arena's own
question above is still open.

---

## Calling a function-typed struct field directly is rejected (likely a real bug)

Found while writing an interaction test (a closure captured in a struct
field of function type - `LANGUAGE.md`'s "First-class functions" section
explicitly lists "a struct field's type" as a legal place for `func(T1, T2)
R` to appear, with no carve-out for calling through one):

```go
struct Callback {
	fn func(int) int
}

func f() int {
	cb := Callback{func(x int) int { return x + 1 }}
	return cb.fn(5)   // sema error: "fn is a field, not a method (cannot be called)"
}
```

`cb.fn` read as a plain value (`g := cb.fn`) type-checks fine and correctly
gets `func(int) int` - the bug is isolated to the *call* dispatch:
`checkCallExpr`'s `funcSigForCall` (`src/sema/typecheck.go`) routes every
`MemberExpr` callee through `methodSigForCallee`, which resolves the member
symbol and immediately rejects anything that isn't a declared method or a
package-qualified function (`"%s is a field, not a method (cannot be
called)"`) - unlike a plain `Ident` callee, which already has exactly the
right fallback (`funcSigForCall`'s own `Ident` case: if it's not a real
declared function, check it as an ordinary value expression and, if its type
is `TypeFunc`, allow an indirect call). `MemberExpr` never gets that same
fallback, so a call through *any* func-typed field is unreachable today, not
just this one example.

Not fixed here - this needs a coordinated sema+codegen change (sema's
`methodSigForCallee`/`funcSigForCall` need a fallback path for a
field-typed callee, and codegen's own mirrored dispatch -
`isDirectFuncCall`/`isPackageQualifiedCall`, `src/codegen/expr.go` - needs a
matching indirect-call case, the same way it already handles a func-typed
`Ident`/parameter), not a one-line fix, and it's not clear whether this was
simply missed when first-class functions/struct fields landed or is a
deliberate scope cut analogous to the "method value... out of scope"
restriction `LANGUAGE.md` documents for methods specifically (which this is
not - `cb.fn` is an ordinary field, not a method value). **Current default
while open:** left rejected as-is;
`src/codegen/interaction_test.go`'s `TestClosureStoredInStructFieldAndReadBackAsValue`
works around it (reads the field into a local before calling) to still get
real interaction coverage of func-typed struct fields.

# Architecture decision log

A dated, append-only log of real design forks in this project - the "why we
did X instead of Y" narrative that doesn't fit anywhere else: too
cross-cutting for a single code comment, not language-spec material for
`LANGUAGE.md`, and not "how to work in this repo" material for `AGENTS.md`.
Entries are never edited to look like they were always right - if a
decision is later reversed, add a new entry that supersedes the old one and
say so, rather than rewriting history.

This is a decision log, not a task tracker - see `BLOCKERS.md` for
currently-open questions still needing a human call, and `AGENTS.md` for the
pointer to all of this project's docs.

---

## 2026-07-22 - Numeric type widths: six concrete kinds, `int` as an alias

**Decision:** add explicit-width integers `i8`/`i16`/`i32`/`i64` and floats
`f32`/`f64`, with `int` kept as a pure alias for `i32` (not a distinct type -
`sema.TypeInt == sema.TypeI32`, literally the same `Type` value) rather than
its own 64-bit type or a separate concept entirely.

**Why:** `main`'s real LLVM signature must return `i32` (the OS process exit
code) - keeping `int == i32` means a source-level `func main() int { return
code }` needs no truncation/cast at all, since the language's own `int` and
the platform C ABI's `int` are simply the same type (see `CODEGEN.md`'s
"`int` is 32-bit" section). No unsigned types were added alongside these -
they weren't requested and bring their own complexity (comparison semantics,
printf specifiers) that's easy to layer in later if actually wanted.

**Status:** shipped. See `LANGUAGE.md`'s Types section for the full rules,
including the untyped-numeric-constant model this made necessary (six
concrete widths made bare literals like `5` ambiguous without Go's own
untyped-constant deferral/defaulting rules).

---

## 2026-07-22 - The arena allocator: one process-lifetime bump allocator, not scoped frees

**Decision:** every codegen-level heap allocation (currently just string
concatenation) goes through one centralized, generated LLVM function
(`llvm_lang.arena_alloc`) that bump-allocates out of malloc'd 64KiB chunks,
grown for the lifetime of the process. No `free`, no refcounting, no GC -
this is a real, intentional, permanent memory leak.

**Why:** this project doesn't have a real memory-management strategy
designed yet (scoped stack-frame frees, refcounting, a tracing GC are all
still on the table - see the open entry in `BLOCKERS.md`), and inventing one
wasn't in scope for landing string concatenation. Centralizing every
allocation behind one primitive now means whichever real strategy gets
chosen later only has one call site to change, instead of having to hunt
down and retrofit scattered ad hoc `malloc` calls across the codebase. This
is explicitly groundwork for that future decision, not an attempt to
preempt it.

**Status:** shipped, and treated as the default allocation path for any
future heap-needing feature (e.g. dynamic arrays) until the real
memory-management question is answered. See `CODEGEN.md`'s "The arena
allocator" section for the mechanics.

---

## 2026-07-22 - First-class functions: fat-pointer `{fnPtr, ctxPtr}` representation

**Decision:** a function value (currently: a free-function reference only)
lowers to a two-pointer LLVM struct `{ fnPtr, ctxPtr }`, not a bare function
pointer. This round, `ctxPtr` is always null and unused - only `fnPtr` does
anything. A direct call (`add(1, 2)` where `add` is a statically-known
function name) bypasses this representation entirely and stays a plain
direct `call`, zero overhead; only a call *through* a function-typed
variable goes through the fat pointer.

**Why:** the user explicitly asked that this representation account for a
future bound-method value (`p.move` referenced without being called) even
though method values are out of scope this round - a bound method value
naturally needs to carry both a function pointer *and* the receiver address
it closes over, which is exactly the `ctxPtr` slot this representation
already has room for. Choosing the fat-pointer shape now means that future
feature can slot into the same representation and calling convention
without a later redesign/migration of every existing function-value site.

**Status:** shipped. Free functions are first-class values (reference,
assign, pass, return, call indirectly); method values remain explicitly out
of scope. See `LANGUAGE.md`'s "First-class functions" section for the
language-level rules and `CODEGEN.md`'s "First-class functions" section for
the fat-pointer construction site (`genFuncValue`) and the direct-vs-
indirect call dispatch (`isDirectFuncCall`/`genIndirectCall`).

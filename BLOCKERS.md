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
dynamic-array growth in a loop. Struct destructors (`LANGUAGE.md`'s
"Destructors" section) now exist and cover one narrow slice of "automatic
cleanup" - a non-copyable type's own scope-exit/`delete`-time cleanup - but
deliberately don't attempt anything like a general GC/refcounting scheme (no
recursive cascading through embedded fields, no move semantics); the arena's
own question above is still open regardless.

---

## Incremental reparse / a real green-red tree for the LSP

Building `src/lsp` (an editor language server) surfaced a real, open design
question: `ast.Tree` today is index/arena-based (`NodeIndex int32` into a
flat `Tree.Nodes` slice, no pointers) - already structurally close to a
Roslyn/rust-analyzer "green tree." But `ast.Node.Span` stores **absolute**
byte offsets, not relative width. A real green tree stores width specifically
so an unedited subtree's identity is position-invariant and structurally
shareable across an edit (the "red tree" is then a thin, lazily-materialized
layer on top that computes absolute positions/parent chains on demand).
Bolting a red layer onto the current absolute-offset representation would not
get that sharing - it would need `ast.Node`'s own representation reworked
first (relative width instead of absolute `Span`, touching the parser and
every `Span` consumer), a genuinely separate, nontrivial project from "add
LSP support."

**Why this isn't inferable/default-able:** it's a real product/performance
tradeoff (how much engineering effort now vs. later, for a benefit that only
matters once reparse-per-edit is measurably too slow), not something with an
obvious "right" default the way most engineering calls in this codebase are.

**Current default (explicitly chosen, not just deferred by omission):**
`src/lsp` re-runs the whole frontend (lexer -> parser -> sema.Resolve/
CheckProgram) on every debounced edit, no incremental reuse at all.
`BENCHMARKS.md`'s own numbers - lexer+parser+sema together at ~238us for a
small fixture, ~3.2ms for a large (40x) one - make this comfortably fast
enough for an interactive editor loop at this project's current scale.
Revisit only once a real, large `.llx` file actually demonstrates
reparse-per-edit is too slow in practice - not speculatively ahead of that.

---

## True suspend/resume coroutines (async/await, game-loop style)

This language's `yield T` generator functions (see `LANGUAGE.md`'s
"Generator functions" section) are deliberately push/callback-lowered, not
real suspend/resume - a generator's own body runs synchronously to
completion (or an early stop) the moment it's ranged over; there is no way
to pause a function mid-execution and resume it later from an unrelated
point in time (a timer firing, a scheduler's own next tick). The user asked
what a *real* coroutine primitive - specifically Unity/C#-style
`StartCoroutine`-flavored async ("in X seconds, run Y, without blocking"),
not the iterator/`range`-flavored kind already built - would actually take.

**What's genuinely hard, and why:** LLVM has real, mature first-class
coroutine support (`llvm.coro.id`/`coro.begin`/`coro.suspend`/`coro.resume`/
`coro.destroy`/`coro.end`, plus a `CoroSplit` pass that splits a function
into resume/destroy entry points backed by one heap-allocated frame) - this
isn't something to build from scratch. Two real risks, investigated but not
yet resolved:

- **Tooling gap**: the vendored `go-llvm` bindings (`third_party/go-llvm`)
  have zero wrapper functions for any `coro.*` intrinsic and no generic
  "declare an intrinsic by name" helper. Very likely still usable - LLVM
  recognizes intrinsics by name against its own table, so
  `llvm.AddFunction("llvm.coro.id", fnType)` + an ordinary `CreateCall`
  (the exact mechanism already used for `printf`/`malloc`/`free`) should
  work - but this is unverified, would need real experimentation (exact
  intrinsic signatures from LLVM's own coroutine docs, confirming
  `RunPasses` accepts `coro-split`/`coro-cleanup` pass names through the C
  API's string pipeline) before committing to the approach.
- **A genuinely new codegen shape, not a sign the existing destructor
  mechanism is wrong**: every destructor-unwind path today
  (`Generator.destructors`, `src/codegen/stmt.go`) is emitted during one
  single, straight-line compile-time pass, reached via normal, statically-
  determined control flow. A coroutine's own "destroy while suspended"
  path is dispatched *dynamically*, at an arbitrary later time, via a
  saved suspend-index - LLVM's coroutine ABI expects the frontend to
  author one cleanup block per suspend point for exactly this case. The
  information needed to write each block (what's on the destructor stack
  at that point) is already fully available at compile time via the
  existing mechanism - `CoroSplit` separately handles ordinary value
  liveness across suspend points automatically, with no frontend
  involvement needed. **The gap is a new consumer of already-tracked
  information (emit N cleanup blocks, dispatched via a switch), not a
  case for refactoring destructor handling itself** - investigated this
  specifically since it looked at first like the destructor mechanism
  might need to become more "formal"/runtime-based; a closer look showed
  the existing compile-time bookkeeping already has what's needed.

**If pursued, the recommended shape** (validated against the user's own
Unity-coroutine framing specifically, not general arbitrary-interleaving
true coroutines): single-threaded and cooperative, matching Unity's real
design - no OS threads, no data races, no thread-safety burden in the
runtime. A coroutine handle modeled as *just another* non-copyable,
destructor-owning value in the existing type system (its own "destructor"
calls `llvm.coro.destroy`), reusing the scope-exit cleanup machinery
already built rather than inventing a new concept. A minimal
stdlib-level scheduler (a sorted list of `(resumeAt, handle)` pairs, driven
by an explicit `Tick(dt)` call from the user's own game loop) plus a small
set of awaitables (`Wait(seconds)`, later `WaitUntil(predicate)`) - both
ordinary runtime/library code, not a hard compiler problem. Deliberately
*not* general "hold and interleave two coroutines by hand" true coroutines
- that pushes real new complexity into handle-type surface syntax for a
capability the motivating use case (game-loop timers) doesn't need.

**Why this isn't inferable/default-able:** a real feature-investment
decision (is async/await-style scheduling worth the compiler-architecture
risk above, given push/callback generators already cover the
iteration/filtering use cases that motivated the whole "iterators" arc) -
not something with an obvious default. **Current status:** not started, no
priority decided - this entry exists so the analysis isn't lost, not
because work is in progress.

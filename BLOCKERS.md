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
cleanup" - a non-copyable type's own scope-exit/`delete`-time cleanup, plus
explicit `move` semantics for handing ownership to a new binding - but
deliberately don't attempt anything like a general GC/refcounting scheme (no
recursive cascading through embedded fields, no partial/field-level moves);
the arena's own question above is still open regardless.

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

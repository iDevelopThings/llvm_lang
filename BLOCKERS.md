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

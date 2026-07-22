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
no `free`, no refcounting, no GC. It was built as groundwork/a centralized
allocation point, not as an answer to whether this language needs a real
memory strategy eventually (scoped stack-frame frees when a value provably
can't escape, refcounting, or a tracing GC are all still on the table, each
with very different implications for the language's semantics and runtime
complexity). Not inferable from established patterns - this is a genuine
design fork only the user can make the call on. **Current default while
open:** keep leaking via the arena; every new heap-needing feature (e.g.
dynamic arrays) routes through the same `arena_alloc` primitive rather than
inventing its own allocation path, so there's still only one call site to
change once this is answered.

---

## Cross-package struct literal construction with an unexported field

Go's own spec is stricter than a plain "unexported names aren't accessible"
rule: constructing a struct value with a *positional* composite literal
(`Point{1, 2}`) from another package is itself rejected if the struct has
*any* unexported field, even one the literal never mentions by name and
even if every field the literal actually supplies is exported - not just an
error at the point an unexported field is later read. Whether this
language should match that exact rule, or something looser/different, isn't
inferable from `AGENTS.md`/`LANGUAGE.md`'s existing patterns - it's a real
design fork over how strict "construction" itself should be, independent of
member-access enforcement (which is unambiguous and already fully
implemented). **Current default while open:** construction is unrestricted
regardless of unexported fields (`mathutils.Point{1, 2}` from another
package is accepted even though `Point` has an unexported field) - only
actual member *access* (`p.secret`) is enforced. This is the most permissive
option, and doesn't block anything: tightening it later only ever adds a
new diagnostic at a specific site (`sema.checkStructCompositeLit`,
`src/sema/typecheck.go`), never removes one, so nothing shipped under the
current default needs to change shape if this is answered stricter later.

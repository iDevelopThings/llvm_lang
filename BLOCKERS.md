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

(No open questions as of this cleanup - every prior entry here was either
a resolved engineering note already captured as a code comment, or a
design fork the user has now directly answered in conversation: numeric
type widths and `int`'s meaning, explicit conversion syntax, first-class
function scope, multi-file export policy, implicit global-var
initialization, and dynamic array construction/growth. See AGENTS.md for
the actual resulting rules once each round of work lands.)

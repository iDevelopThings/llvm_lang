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

## 2026-07-25 - Generics: unconstrained monomorphization, not Go-style constrained generics

**Decision:** generics are C++-template-shaped - type parameters carry no
constraints at all, and a generic body is type-checked once per concrete
instantiation, against that instantiation's real types. Not Go's model
(interface-constrained type parameters, one body checked once against the
constraint, dictionary/GC-shape-stenciled at runtime).

**Why:** Go's model is inseparable from interfaces, and this language has
none - adopting it would have meant designing and building an entire
trait/interface system first, purely as scaffolding for generics, before a
single generic function could be written. Unconstrained monomorphization
needs none of that: substitution *is* the whole mechanism. It also matches
what the feature is actually for here (a `SlotMap[T]` over concrete game
data), and it makes every instantiation zero-overhead by construction, which
matters for a compiler that already pays cgo/LLVM costs.

The cost is the C++ cost, accepted knowingly: an error inside a generic body
is reported at the instantiation that triggered it rather than at the
declaration, and a generic nobody instantiates is never checked at all.

**Rejected alternative:** keep one shared AST and thread a substitution map
through every type lookup in sema *and* codegen. Cloning the template's
subtree per instantiation (the mechanism, see `CODEGEN.md`'s "Generics"
section) keeps per-instantiation awareness out of codegen entirely, which the
substitution-map approach would have spread across thousands of lines of it -
against this project's "sema decides, codegen consumes `Info`" rule.

**Status:** shipped. See `LANGUAGE.md`'s "Generics" section for the rules.

---

## 2026-07-22 - Numeric type widths: six concrete kinds, `int` as an alias

**Decision:** add explicit-width integers `i8`/`i16`/`i32`/`i64` and floats
`f32`/`f64`, with `int` kept as a pure alias for `i32` (`sema.TypeInt ==
sema.TypeI32`, literally the same `Type` value), not its own 64-bit type or a
separate concept.

**Why:** `main`'s real LLVM signature must return `i32` (the OS process exit
code) - keeping `int == i32` means `func main() int { return code }` needs no
truncation/cast at all. No unsigned types were added - not requested, and
they bring their own complexity (comparison semantics, printf specifiers)
that's easy to layer in later if actually wanted.

**Status:** shipped. See `LANGUAGE.md`'s Types section for the full rules,
including the untyped-numeric-constant model six concrete widths made
necessary.

---

## 2026-07-22 - The arena allocator: one process-lifetime bump allocator, not scoped frees

**Decision:** every codegen-level heap allocation (currently just string
concatenation) goes through one centralized, generated LLVM function
(`llvm_lang.arena_alloc`) that bump-allocates out of malloc'd 64KiB chunks,
grown for the process's lifetime. No `free`, no refcounting, no GC - this is
a real, intentional, permanent memory leak.

**Why:** this project doesn't have a real memory-management strategy
designed yet (see the open entry in `BLOCKERS.md`), and inventing one wasn't
in scope for landing string concatenation. Centralizing every allocation
behind one primitive means whichever real strategy gets chosen later only
has one call site to change, instead of scattered ad hoc `malloc` calls.

**Status:** shipped, and treated as the default allocation path for any
future heap-needing feature (e.g. dynamic arrays) until memory management is
answered. See `CODEGEN.md`'s "The arena allocator" section for the mechanics.

---

## 2026-07-22 - First-class functions: fat-pointer `{fnPtr, ctxPtr}` representation

**Decision:** a function value (currently: a free-function reference only)
lowers to a two-pointer LLVM struct `{ fnPtr, ctxPtr }`, not a bare function
pointer. This round, `ctxPtr` is always null and unused - only `fnPtr` does
anything. A direct call (`add(1, 2)`) bypasses this representation entirely
and stays a plain direct `call`, zero overhead; only a call *through* a
function-typed variable goes through the fat pointer.

**Why:** the user asked that this representation account for a future
bound-method value (`p.move` referenced without being called) even though
method values are out of scope this round - a bound method naturally needs
both a function pointer and the receiver address it closes over, exactly the
`ctxPtr` slot this representation already has room for, avoiding a later
redesign.

**Status:** shipped. Free functions are first-class values (reference,
assign, pass, return, call indirectly); method values remain out of scope.
See `LANGUAGE.md`'s "First-class functions" section and `CODEGEN.md`'s
section of the same name (`genFuncValue`, `isDirectFuncCall`/`genIndirectCall`).

Note (superseded): the "always null" claim above no longer holds once the
"Lambdas" entry further down added real closures, whose `ctxPtr` genuinely
carries a non-null capture-context pointer through an indirect call. See that
entry for current behavior; this entry is left as-is, historical-log style.

---

## 2026-07-22 - Multi-file packages: directory = package, non-recursive

**Decision:** a package is exactly "every `.llx` file directly inside one
directory" - Go's own model, adopted as-is. Explicitly non-recursive: a
subdirectory's `.llx` files are never pulled in, even implicitly.
`llvmc some/dir/main.llx` and `llvmc some/dir` compile the identical file set
(a file argument resolves to its own containing directory - see
`src/loader`).

**Why:** Go developers already know exactly what "package = directory, no
recursion" means, and this language is already deliberately Go-flavored
throughout. A manifest or recursive package tree would add real design
surface for a problem Go already has a well-understood answer to.
Non-recursive avoids a subdirectory silently becoming part of a package it
wasn't obviously meant to belong to (e.g. a `testdata/`-style subfolder).

**Status:** shipped. See `LANGUAGE.md`'s "Multi-file packages" section and
this round's `examples/multifile/`. Cross-package `import` syntax and the
`sema.Symbol.Exported` hook becoming enforced are out of scope - a single
package is still the only unit that exists right now.

---

## 2026-07-22 - Multi-file packages: one shared Module per package, not one Module per file

**Decision:** `codegen.GeneratePackage` lowers every file in a package into
one single `llvm.Module`, never one `llvm.Module` per file linked together
afterward.

**Why:** every file in a package always ends up needing to call into, and be
called from, every other file in that package - there is no "private to this
file" concept yet (`Exported` isn't enforced this round) - so a per-file
design would need every cross-file call to go through an external-linkage
declaration plus a real link step, purely to re-assemble what one shared
module gives for free. This compiler is the only producer of every module in
play, so there's no requirement to keep files as separate compilation units
at the LLVM IR level - only a *frontend* file/tree distinction is required
(for diagnostics, and so `ast.NodeIndex` stays meaningful).

**Status:** shipped. See `CODEGEN.md`'s "Multi-file packages: one shared
Module per package" section - `Generator.funcs`/`globals`/`structLayouts` are
all keyed by `*sema.Symbol`/`*sema.StructInfo` pointer identity, not
`ast.NodeIndex`, which is what makes a shared module free of extra cross-file
plumbing.

---

## 2026-07-22 - Adopting `afero` for file loading

**Decision:** all disk I/O this compiler needs for multi-file package loading
(`src/loader`) goes through `github.com/spf13/afero`'s `afero.Fs` interface
rather than calling `os.ReadFile`/`os.ReadDir`/`os.Stat` directly. Production
wires in `afero.NewOsFs()`; tests build fake package layouts with
`afero.NewMemMapFs()`.

**Why:** `src/loader`'s own test suite needs to exercise several directory
shapes (multiple files, an empty directory, a file resolving to its
containing directory, an unreadable file) that would otherwise mean creating
and tearing down real temp directories for every test case. An in-memory
`afero.Fs` gives `Load` the exact same `Stat`/`ReadDir`/`Open`-shaped
interface regardless of implementation, so production and test code share the
identical code path, not a mocked variant that could drift.

**Status:** shipped, and adopted as a standing convention going forward, not
just for this one package - see `AGENTS.md`'s "Standards" section: any future
disk I/O should go through `afero.Fs` for the same testability reason.

---

## 2026-07-22 - Cross-package imports: relative-path resolution, not a module-root/manifest scheme

**Decision:** an `import "path"` is resolved relative to the *importing
file's own directory* - confirmed directly with the user, not inferred.
`./mathutils` written in `app/main.llx` resolves to `app/mathutils`; a
different file in a different directory resolves relative to *its own*
directory instead. There is no project/module root, no manifest naming a
module path, and no absolute "package path" concept at all.

**Why:** the simplest scheme that still fully supports the one thing this
round needs - a module-root/manifest scheme (Go modules, `go.mod`-style) adds
real design surface (where the root lives, what syntax names a module, how a
package path maps back to a directory) for a problem this project doesn't
have yet: there's still no multi-repository/external-dependency story at
all. Relative-path resolution reuses `src/loader`'s already-existing
directory-resolution logic almost as-is, extended to recurse.

**Status:** shipped. See `LANGUAGE.md`'s "Imports" section and
`src/loader/program.go`'s `LoadProgram`.

---

## 2026-07-22 - Imports: one shared Module for the whole program, not real separate compilation

**Decision:** extending "one shared Module per package" (see the two entries
above) to "one shared Module for the entire program" - every package
reachable via the import graph still lowers into the exact same single
`llvm.Module`, not separate per-package modules with a real link step.

**Why:** identical reasoning to the per-package decision, one level up: this
compiler is still the only producer of every module in play, and every
cross-file lookup codegen needs was already keyed by symbol/struct pointer
identity rather than by file or package. Verified, not assumed:
`genPackage`'s five passes needed zero changes to correctly handle a
multi-package program, since none of them have any notion of "package" - they
just iterate every tree passed in. Real separate compilation (an object-file
backend, a real linker) isn't a need this project has yet.

**Status:** shipped. See `CODEGEN.md`'s "Imports: still one shared Module,
now for the whole program" section.

---

## 2026-07-22 - Imports: no aliasing syntax this round

**Decision:** `import "./mathutils"` always binds its path's own last segment
as the local name (`mathutils`) - there is no `import m "./mathutils"` form
to pick a different local name.

**Why:** not needed for this round to be a complete, usable feature - every
real use case this round targets works fine without it. Deliberately
deferred rather than designed-then-unused: aliasing is a small, additive
grammar extension (an optional identifier before the path string) that can
be layered on later with no rework of the path-resolution/binding machinery
this round already built.

**Status:** deferred, not shipped. See `LANGUAGE.md`'s "Imports" section.

---

## 2026-07-22 - Struct constructors: overloading scoped to constructors only, no Go precedent

**Decision:** a struct may declare one or more `constructor(params) { body }`
blocks, invoked via `Name(args)` call syntax (distinct from the unchanged
`Name{...}` composite literal). Multiple constructors on the same struct are
overloaded by argument count only - not by argument type - and this
overloading is explicitly, deliberately scoped to constructors alone: it is
**not** a precedent for general function/method overloading anywhere else.
Two free functions or two methods sharing a name remain a redeclaration
error, unchanged.

**Why:** a genuine language-design fork with no Go precedent to fall back
on, unlike almost everything else built this session (numeric widths,
first-class functions, multi-file packages, imports - each had a direct Go
analogue to adopt as-is). Go itself has no constructors and no overloading of
any kind. The user's own reasoning: scoping the overload resolution to
constructors keeps it "bound to the type explicit and differ it from a
regular function" - a constructor call is already type-directed (its own
struct type name is the callee), so overloading it by arity is a contained,
narrow special case rather than an opening for arbitrary function/method
overloading, which brings its own much larger set of design questions (name
mangling, overload resolution across argument *types*, interaction with
first-class function values) never asked for.

**Status:** shipped. See `LANGUAGE.md`'s "Constructors" section and
`CODEGEN.md`'s "Constructors" section for the lowering (each constructor
becomes its own real LLVM function, named `Struct.constructor.N`).

---

## 2026-07-22 - Dynamic arrays: `append` scoped to exactly one element per call

**Decision:** `append(slice, elem)` takes exactly one element to append, not
Go's full variadic `append(s, e1, e2, ...)` form.

**Why:** this language has no variadic functions anywhere yet, and inventing
that machinery purely to give `append` a multi-element form was out of scope
for this round. Appending several elements is simply several calls (`s =
append(s, a); s = append(s, b)`), more verbose but no less correct - a
natural extension point once (if) variadic functions are ever designed
generally.

**Status:** shipped. See `LANGUAGE.md`'s "Dynamic arrays" section and
`CODEGEN.md`'s section of the same name for `genAppendCall`'s lowering.

---

## 2026-07-22 - Dynamic arrays: capacity growth by simple doubling

**Decision:** `append`'s growth strategy, once a slice's `len == cap`, is
`newcap = max(1, cap*2)` - the simplest possible doubling strategy, not Go's
own more elaborate growth curve (which slows for larger slices and has
changed more than once across Go's own releases).

**Why:** this project doesn't have a performance-sensitive workload driving
slice growth yet, and Go's tuned curve is itself an incidental implementation
detail, not a language-semantic guarantee - nothing about `append`'s
observable behavior depends on *which* growth curve is used, only on `cap`
ending up large enough. Simple doubling is the smallest correct
implementation satisfying that contract, and an easy default to revisit if a
real workload ever demonstrates it matters.

**Status:** shipped. See `CODEGEN.md`'s "Dynamic arrays" section for
`genAppendCall`'s exact lowering (`newcap = max(1, cap*2)`, via a `select` on
`cap*2 < 1`).

---

## 2026-07-22 - Lambdas: a uniform, `ctxPtr`-first ABI for every indirectly-called function value

**Decision:** once genuine closures (function-literal expressions with
by-reference capture) exist, a free function referenced as a bare value no
longer puts its own real function address into the fat pointer's `fnPtr`
field - it puts the address of a small, per-function, memoized adapter
("thunk") instead, with an extra leading `ctxPtr` parameter it simply ignores
before calling straight through to the real function. Every genuine lambda's
own synthesized function already has this `ctxPtr`-first shape natively (it
needs to dereference `ctxPtr` to reach its captures). Every *indirect* call
now unconditionally extracts and passes `ctxPtr` along as the real callee's
first argument; a *direct* call to a statically-known function name is
completely unaffected - it never touches the fat pointer.

**Why:** a single `func(T1, T2) R`-typed variable can hold either a plain
free-function reference or a genuine closure at runtime, and an indirect
call has no way to tell which before it emits its one call instruction. But
the two kinds' *real* underlying LLVM functions have genuinely different
signatures: a free function's has no `ctxPtr` (necessary to keep a direct
call zero-overhead), while a lambda's must take a real, dereferenced
`ctxPtr`. Calling through a function pointer whose real callee has a
different real parameter list than the call site built is invalid,
UB-risking IR that can silently corrupt the stack/registers rather than fail
cleanly. A per-free-function thunk, built lazily and memoized, is the
standard technique real closure-supporting language implementations use for
this "uniform calling convention across heterogeneous callees" problem - it
costs nothing for a direct call (the thunk isn't built unless the function's
address is taken as a value) and only a small adapter call for an indirect
one.

**Status:** shipped. See `CODEGEN.md`'s "Lambdas" section (the "uniform-ABI
thunk" subsection) for the exact mechanism (`genFuncThunk`/`genFuncLit`/
`genIndirectCall`), and `TestUniformAbiAcrossPlainFunctionAndLambda`
(`src/codegen/lambda_test.go`) for the regression test exercising a single
func-typed variable holding each kind of value in turn, calling it indirectly
both times.

---

## 2026-07-22 - Pointers: `new`/`delete` get a real, separate heap from the arena

**Decision:** `new T(args)`/`new T{...}` mallocs its own individually-sized
block directly (a plain libc `malloc` call, not routed through
`llvm_lang.arena_alloc`), and `delete p` frees exactly that block via a real
libc `free` call. The bump-allocator arena (string concatenation, dynamic
arrays) is completely untouched by either - two genuinely separate heaps,
not one heap with two access paths into it.

**Why:** the arena is a bump allocator with no notion of "give this one
sub-allocation back" at all - freeing a single `new`'d value out of a chunk
other, still-live allocations share would either be a no-op or require
retrofitting real free-list bookkeeping onto a primitive deliberately kept
simple (string concatenation/dynamic arrays leak for the process's lifetime
by design, an accepted trade-off). Reusing a bare `malloc`/`free` pair needs
none of that: the direct, obviously-correct mapping from "the user asked for
exactly this much heap memory back" to the real system call, with clean
per-allocation semantics that don't complicate the arena's own bump-pointer
invariants.

**Status:** shipped. See `CODEGEN.md`'s "Pointers" section for the mechanism
(`genNewExpr`/`genDeleteStmt`). Destructors/RAII (running a struct's own
cleanup logic automatically at `delete` time) are out of scope - `delete`
only frees raw memory; there is no such concept in this language yet.

---

## 2026-07-22 - Pointers: `nil` scoped to pointer types only, not a general zero value

**Decision:** `nil` is a predeclared, untyped placeholder value usable only
where a pointer type is expected (a `*T` variable's initializer, either side
of `==`/`!=` against a pointer) - it is not a general "zero value" concept
usable against a struct, array, numeric, string, or bool type the way a Go
`interface{}` holding `nil` or a `nil` map/slice would suggest.

**Why:** this language has no interface/`any` type and no reference-typed
non-pointer value (a struct/array/string is always a real concrete value,
never something that could itself be absent) - the *only* thing that can
meaningfully have "no value" is a pointer. Modeling it as its own narrow
untyped-constant kind (`sema.TypeUntypedNil`, deliberately kept out of the
existing `IsUntyped()`/`IsNumeric()` predicates) reused this language's
existing untyped-constant deferral machinery almost entirely as-is. Unlike an
untyped numeric constant, `nil` was deliberately given **no default type** -
Go allows `var x interface{} = nil` because `interface{}` is nil's own
natural home; this language has nothing equivalent, so a context that never
pins down a concrete `*T` (`p := nil`) is a real error rather than a silently
accepted default.

**Status:** shipped. See `LANGUAGE.md`'s "Pointers" section for the exact
rules and `sema/pointer_test.go` for the coverage (bare `:= nil`, `nil ==
nil`, `nil` against a non-pointer, all rejected).

---

## 2026-07-22 - Pointers: auto-deref scoped to member access only, not indexing

**Decision:** `p.field`/`p.method(...)` on a `*T` auto-dereferences (behaves
exactly like `(*p).field`/`(*p).method(...)`), matching Go's own automatic
pointer-dereference rule for selector expressions - but indexing does not:
`p[0]` on a `*[N]T` is rejected; `(*p)[0]` is required.

**Why:** Go itself only auto-derefs for selector expressions, never for
indexing (`p[0]` on a `*[N]T` is also a compile error in real Go) - this is
matching the same precedent exactly, not a narrower carve-out invented here.
Member access auto-deref pulls its weight because a pointer-to-struct is the
overwhelmingly common shape once `new` exists at all; forcing `(*p).field`
everywhere would make `new` noticeably more awkward for no real benefit.
Indexing through a pointer-to-array is rarer, and Go's own choice not to
special-case it suggests the inconsistency-avoidance isn't worth it there
either - extending auto-deref to indexing was simply out of scope.

**Status:** shipped, scoped as described above. See `LANGUAGE.md`'s
"Pointers" section and `sema/pointer_test.go`'s
`TestNewCompositeLitProducesPointer` for the explicit `(*a)[0]` case this
implies.

---

## 2026-07-22 - Non-constant global initializers: declaration order, not a dependency-graph topological sort

**Decision:** every non-constant top-level `var`'s real initializer now runs
in one synthesized init function, in plain **source declaration order**
across the whole package - not a full dependency-graph topological sort the
way Go's own spec actually requires (Go orders strictly by each variable's
dependencies, so `var a = b + 1; var b = 2` initializes `b` before `a`
regardless of which is written first).

**Why:** a real topological sort needs a dependency graph built from which
other globals each initializer's expression references (transitively,
through any function it calls too), plus cycle detection - genuinely more
analysis machinery than this round's actual goal (lifting the
"must-be-a-compile-time-constant" restriction) needed to justify building
right away. Declaration order is simple, predictable, and correct for the
overwhelmingly common case - it only diverges from Go's real semantics when
a global's initializer references another global declared *later* in the
same package, which now deterministically observes that other global's zero
value rather than Go's dependency-based result.

**Status:** shipped, with this scoping deliberately narrower than Go's real
behavior - see `CODEGEN.md`'s "Global var initializers" section for the exact
mechanism (`@llvm.global_ctors` plus one synthesized `llvm_lang.global_init`
function) and `src/codegen/globals_test.go`'s
`TestGlobalNonConstantInitializersRunInDeclarationOrder`. A real
dependency-graph sort (matching Go's spec exactly) remains a reasonable
future upgrade if a program ever needs it.

---

## 2026-07-22 - Slicing: reusing `{ptr, len, cap}`/`{ptr, len}` directly, and a range check generalized from `genBoundsCheck`

**Decision:** a slice expression (`s[a:b]`) never allocates or copies
anything - it builds a fresh `{ptr, len, cap}` (dynamic array) or `{ptr,
len}` (string) value directly from its operand's own already-existing fields
(or, for a fixed array, its own real address), reusing the exact same struct
shapes `make`/string literals already use. The single-index bounds check
(`genBoundsCheck`) is generalized to a genuine range check
(`genSliceRangeCheck`: `0 <= low <= high <= max`) rather than reusing
`genBoundsCheck` twice - two separate single-bound checks can't express `low
<= high` at all (`s[3:1]` must trap even though both `3` and `1` are
individually in-range for a length-5 slice).

The one genuinely non-obvious wrinkle: for a dynamic array specifically, the
omitted-high *default* and the range check's own upper *bound* are
deliberately two different values - `len(s)` and `cap(s)`, respectively.
Getting this backwards (defaulting the high bound to `cap(s)`) would silently
expose a slice's spare capacity to any bare `s[a:]`, which is not what Go
itself does (confirmed against Go's own spec: an upper bound is allowed to
reach into spare capacity only when written explicitly, never by omission).
`genSliceBounds` threads `defaultHigh` and `max` as two separate parameters
for exactly this reason, even though they coincide for a string/fixed-array
operand.

**Why:** no allocation/copy matches this feature's entire reason for
existing (a slice expression produces a new header value sharing the same
backing memory as the original). Reusing the existing struct shapes needed
no new LLVM type and no change to `len`/`append`'s own existing logic at all.
A dedicated range-check helper keeps `genBoundsCheck`'s own existing call
sites (plain indexing) completely unchanged, while still sharing its
trap/`unreachable` mechanism.

**Status:** shipped. See `CODEGEN.md`'s "Slicing" section for the three
lowering paths (`genDynArraySlice`/`genStringSlice`/`genFixedArraySlice`) and
`TestSliceDynamicArrayReslicePastLenWithinCap`
(`src/codegen/slice_test.go`), the regression test for the len-vs-cap default
direction.

---

## 2026-07-22 - Destructors: blanket non-copyable rejection instead of move semantics/last-use analysis

**Decision:** a struct may declare one `destructor() { body }`, and declaring
one makes that struct (and anything transitively embedding it by value)
**non-copyable, full stop** - every copy of an *existing* live value is
rejected (`b := a`/`b = a`, a by-value field/array-element store), with
**no** "this happens to be the last use of the variable" leniency anywhere.
A destructor fires at exactly two triggers - a plain local/parameter's own
scope exit (return/break/continue/fall-through, reverse declaration order)
and `delete` against a pointer to it - and does **not** automatically
cascade into a by-value-embedded field lacking its own destructor.

**Why (non-copyable rule):** the alternative - real move semantics (a
value's ownership transfers on its last use) or a last-use/liveness analysis
- is substantially harder: it needs a real liveness/escape analysis over the
whole function body, a way to mark a moved-from binding unusable, and
careful interaction with every construct that can alias a value. Blanket
non-copyable rejection sidesteps the double-destruction problem entirely (if
a value can never be duplicated, there is only ever one instance, so "when
does it destruct" is never ambiguous). The one deliberate carve-out - a
*fresh* composite-literal/constructor-call/`new` construction is never "a
copy" - falls out of the same reasoning: constructing the one instance isn't
duplicating an existing one.

Argument-passing gets this same fresh-construction exception (unlike a
return statement, which allows none at all, even a fresh value) for a
related but distinct reason: a fresh argument's soundness is entirely local
to the one call expression (the callee's own parameter becomes the value's
sole owner), whereas soundly allowing a fresh *return* would require knowing,
at every call site, that the callee always hands back a freshly-owned
instance - a real escape-analysis question, avoided by simply not allowing
it. This is what forces a resource-owning, destructor-having type to only
really move across a function boundary through a `new`'d pointer (the
deliberate, accepted trade-off named in `LANGUAGE.md`'s "Destructors"
section).

**Why (embedded fields):** cascading destruction through arbitrary
by-value-embedded fields (and, transitively, fields-of-fields, arrays of
structs, etc.) is real, general RAII - a much larger feature than this
round's, deliberately avoided. The documented pattern for a resource-owning
type stays simple instead: hold a `*T` **pointer** field to what it owns, and
manually `delete` it from the containing struct's own `destructor()` body
(see the `FileHandle` example in `LANGUAGE.md`) - one level of manual wiring,
not a general cascading mechanism.

**Status:** shipped. See `LANGUAGE.md`'s "Destructors" section for the full
language-level rule and `CODEGEN.md`'s "Destructors" section for the
lowering - in particular `genIfStmt`'s then/else destructor-stack
save/restore, caught directly by `TestDestructorFiresOnFallThroughReturn`/
`TestDestructorFiresOnBreak` (`src/codegen/destructor_test.go`) failing
before that fix existed, since `if`/`else` are alternate, mutually-exclusive
codegen-time continuations, not a sequential continuation of each other the
way two statements in one `Block` are.

---

## 2026-07-23 - JIT execution: LLJIT (ORCv2) instead of the legacy MCJIT `ExecutionEngine`

**Decision:** `cmd/llvmc`'s `jitRunMain`, and every JIT-executing test helper
in `src/codegen` (`compileAndJIT`, `compilePackageAndJIT`,
`compileProgramAndJIT`), now JIT-execute through `go-llvm`'s LLJIT bindings
(ORCv2, LLVM's current JIT infrastructure) instead of the legacy MCJIT-based
`ExecutionEngine` this project used until now.

**Why:** MCJIT is unmaintained upstream - LLVM itself documents ORCv2/LLJIT
as its replacement. The switch surfaced one real, non-drop-in gap: LLJIT has
no equivalent of `ExecutionEngine.RunStaticConstructors()`, which this
project relied on to run `@llvm.global_ctors` (see `CODEGEN.md`'s "Global var
initializers" section) before `main`. Solved by looking up and calling
`llvm_lang.global_init` directly by name instead - the exact same
synthesized function `@llvm.global_ctors`'s own single entry already points
at. That needed one companion change: `buildGlobalInitFn` no longer gives
that function private linkage, since a private symbol has no name a JIT's
`Lookup` can resolve - it now keeps `AddFunction`'s own default (external)
linkage, like every other language-level function. `@llvm.global_ctors`
itself is left in place, unused by the JIT path, since it's still needed for
a real linked/loaded program's C runtime startup sequence.

The disposal/ownership model also changed shape: MCJIT's
`NewExecutionEngine` only ever took ownership of the `Module`, leaving the
owning `Context` for the caller to dispose separately (two calls, two
failure modes). LLJIT's `ThreadSafeContext`/`ThreadSafeModule` wrapping
instead folds both into the LLJIT instance's own ownership, so disposing it
(`jit.Dispose()`) alone tears down the module and context together, in the
correct order.

Two further real, empirically-verified gotchas surfaced while making the
switch - see `CODEGEN.md`'s "A MinGW/GCC ABI quirk" section for the full
write-up of both:

- LLVM's backend auto-inserts a call to `__main()` at the very start of any
  function literally named `main`, when compiling for this project's own
  `*-windows-gnu` (mingw64) host - a real MinGW/GCC ABI compatibility
  convention that MCJIT's own target selection apparently never triggered.
  Worked around by binding `__main` to libc's own `rand` via
  `AbsoluteSymbols`/`JITDylib.Define`.
- LLJIT's compile layer empties the source IR module out once compiled to
  machine code, unlike MCJIT (which kept it intact for its whole lifetime) -
  calling `Module.String()` again after a JIT-executed call returns just the
  bare module header, and this was observed to crash outright in at least
  one case. `src/codegen`'s `jitModule` test helper now captures a module's
  IR text once, up front, before it's ever handed to an LLJIT instance.

The actual "call a JIT'd function" mechanism didn't change: `LLJIT.Lookup`
hands back a raw address exactly like `ExecutionEngine.GetFunctionAddress`
did, so the existing `syscall.SyscallN`-based approach carries over
unchanged.

**Status:** shipped. See `CODEGEN.md`'s "Global `var` initializers" and "A
non-obvious disposal detail" sections for the mechanics.

---

## 2026-07-23 - For-loop header variables: per-iteration capture (Go 1.22+), diverging from this project's own prior implicit behavior

**Decision:** a `for` loop's own init-clause variable (`for i := 0; ...;
i++`), when captured by a lambda created inside the loop's body, now gets a
fresh per-iteration value - confirmed with the project owner as a deliberate
divergence from this project's own prior, pre-1.22-Go-style behavior (one
shared slot, mutated in place by the post-clause) to match modern Go instead.

**Why:** the shared-slot behavior is exactly Go's own infamous
closures-in-a-loop gotcha - confirmed by manual dogfooding: `for i := 0; i <
5; i++ { fns = append(fns, func() int { return i }) }` printed `5,5,5,5,5`
instead of the obviously-intended `0,1,2,3,4`. Go itself changed this exact
behavior in 1.22; there was no reason for a brand-new language to
deliberately re-introduce a mistake an established one already spent years
walking back. The root cause was a codegen accident, not a deliberate
semantic choice: `genForStmt` only ever calls `genStmt` on the init clause
once, before the loop's own `for.body`/`for.post` basic blocks even exist -
so a captured init variable's arena-heap slot only ever gets written to by
that one `arena_alloc` call, unlike a variable freshly declared *inside* the
loop body, whose declaring statement genuinely re-executes on every dynamic
iteration.

**What changed:** `genForStmt` now does two symmetric hand-offs around the
loop body, only for an eligible variable (init declares exactly one name,
`sym.Captured` is true, and its type isn't non-copyable): entering the body,
it copies the loop variable's current value into a fresh arena slot and
repoints `g.locals[sym]` at it, so the body (and any lambda inside it) reads/
writes that iteration's own private copy; entering the post-clause block
(reached by both the ordinary fallthrough and every `continue`), it copies
that value back into the loop variable's real, original storage before the
post-statement (`i++`) runs, so the condition and post-clause keep observing
the one real slot exactly as before. `break` bypasses this entirely, needing
no special handling.

Deliberately excluded: a non-copyable loop variable never gets this
treatment, falling back to the exact shared-slot behavior instead - giving it
fresh per-iteration semantics would mean an implicit copy once per iteration,
silently violating this project's own "non-copyable, zero exceptions" rule.
Nothing today can actually construct this case, so this is a defensive guard
against a future grammar change, not a live gap.

**Status:** shipped. See `LANGUAGE.md`'s "Lambdas" section for the
user-facing semantics and `src/codegen/stmt.go`'s `genForStmt`/
`typeIsNonCopyable` for the implementation, with new coverage in
`src/codegen/lambda_test.go` (`TestForLoopCapturedHeaderVariableGetsPerIterationValue`
and its sibling tests covering `continue`, `break`, and nested loops).

---

## 2026-07-23 - External functions (FFI): a general `extern func` mechanism instead of one-off builtins

**Decision:** add a general-purpose FFI declaration, `extern func Name(params)
RetType` at top level - a real, user-declarable binding to an external C
symbol, resolved at JIT-execution time - rather than hand-rolling one more
bespoke compiler builtin the way `print`/`make`/`append`/`len` already are.

**Why:** the immediate need (`ScopeTimer`, a RAII-style timing helper,
needing Windows' `QueryPerformanceCounter`/`QueryPerformanceFrequency`) could
have been solved with a fifth hardcoded builtin, exactly like the existing
four - but every one of those already required its own bespoke parser/sema/
codegen special-casing, and a fifth one-off would only buy exactly one more
function, forever. A general mechanism costs barely more to build (this
project's own internal externs - `printf`/`malloc`/`free`/`memcpy`/`memcmp`/
`memset`/`llvm.trap` - were already just `llvm.AddFunction` calls with no
body; this feature is that same primitive, made user-declarable) and buys
something the one-off approach never could: a future "standard library" can
simply be ordinary `.llx` packages wrapping `extern func` declarations behind
the existing import system - no new language concept needed for that, ever
again.

A brand-new, separate `ExternFuncDecl` AST node kind was chosen over a
nullable-body `FuncDecl` variant specifically because `FuncDecl`'s own
always-has-a-body invariant is depended on unconditionally by a large amount
of already-shipped code (`resolveFuncBody`, `checkFuncDecl`'s return-flow
analysis, `genFuncBody`'s whole lowering pass) - matching this project's own
established precedent (`ConstructorDecl`/`DestructorDecl` were each their own
new node kind for the identical reason).

This round's scope was deliberately narrowed on several axes:

- **Type-allowlist, not general marshaling.** Only a numeric type, `bool`, or
  a pointer type may cross an extern func's signature - `string`, a struct by
  value, a dynamic array, and a function type are all rejected with a real
  diagnostic. Each of the four rejected shapes is really a small fat struct/
  closure in this compiler's own representation, not a single scalar/pointer
  value a real C caller would recognize.
- **No rename/alias syntax.** The declared name is the linked symbol name,
  verbatim, this round - deferred with no concrete motivating case yet.
- **No variadic extern functions, no `extern var`.** Neither was needed by
  the motivating case; both are separate mechanisms with their own design
  questions not worth answering speculatively.
- **No non-Windows platform consideration.** This project currently only
  targets Windows/mingw64 at all - there is no second platform yet for a
  platform-conditional extern declaration to matter against.

**Status:** shipped. See `LANGUAGE.md`'s "External functions (FFI)" section
for the full language-level rule and `CODEGEN.md`'s own section of the same
name for the lowering (`declareExternFuncSignature` populates the exact same
`Generator.funcs` map an ordinary `FuncDecl` does, so every existing
direct-call codegen path needed zero changes), plus the new
`examples/scope_timer` worked example and test coverage across
`src/parser`/`src/sema`/`src/codegen`.

---

## 2026-07-23 - AOT compilation (`-o`): shelling out to `gcc`, not a vendored/hand-written linker

**Decision:** `llvmc -o <output> <program>` emits a native object file via
the vendored `go-llvm` bindings' already-present target-machine support
(`Target.CreateTargetMachine`/`TargetMachine.EmitToMemoryBuffer`), writes it
to a temporary file, and links it into a real `.exe` by shelling out to
`gcc` - the same mingw64 toolchain this project already requires on `PATH`
for cgo/dev work - rather than reimplementing a linker of this project's own,
or vendoring one (e.g. LLVM's own `lld`).

**Why:** this project's entire toolchain story already depends on mingw64
being present on `PATH` for an unrelated reason (cgo needs `gcc`/`g++` to
build against `libLLVM-22.dll` itself) - requiring it again for linking adds
no new environmental dependency at all, just a new use for one already
required. Writing even a minimal linker (understanding PE/COFF object format
and mingw64's own CRT startup/import-library conventions) is a substantial,
genuinely separate engineering effort this round had no reason to take on
when `gcc` already solves it completely, for free. This also resolves
ordinary libc symbols and any user-declared `extern func` binding to a real
Win32 API export automatically through mingw64's own standard import
libraries - confirmed concretely (`TestBinary_AOT_ExternFuncScopeTimer`), not
assumed.

**The temporary object file goes through a plain `os.CreateTemp`/`os.Remove`,
not this project's own `afero.Fs` convention:** that convention exists
specifically so `src/loader`'s own tests can fake a package's *input* file
layout - a concern that doesn't apply here. This is a single, ephemeral,
write-only scratch file for a CLI-only link step, with no test needing to
fake its contents, immediately removed once `gcc` has read it - a narrow,
deliberate, explicitly-documented exception.

**`main`'s own LLVM signature needed no change at all - verified concretely,
not assumed.** Giving `main` a real `(argc, argv)` parameter pair for the AOT
path would have forced every one of the dozens of existing zero-argument raw
`syscall.SyscallN` call sites (`jitRunMain`, this package's own
`jm.runInt32(t, "main")` test call sites) to suddenly pass two real
arguments instead of none - a real regression risk explicitly avoided once a
working alternative existed (see the `args()` entry below). Confirmed
directly: `TestBinary_AOT_HelloWorld` et al. AOT-compile and run real,
standalone executables successfully with `main`'s signature completely
unchanged - mingw64's own CRT calling a zero-parameter `main()` with
argc/argv/envp it simply never reads is ordinary, valid C-ABI behavior.

**The `-o` flag itself** was named to mirror gcc/clang's own long-established
convention for exactly this purpose (`gcc foo.c -o foo`), consistent with
this project's own existing `-emit-llvm` flag precedent.

**Status:** shipped. See `CODEGEN.md`'s new "`-o`: AOT compilation to a
native executable" section for the full mechanism (`compileToExecutable`)
and `cmd/llvmc/main_test.go`'s `TestBinary_AOT_HelloWorld`/
`TestBinary_AOT_Features`/`TestBinary_AOT_ExternFuncScopeTimer`/
`TestBinary_AOT_Args` for the real, standalone-process acceptance tests this
round's own verification leaned on.

---

## 2026-07-23 - `args()` builtin: `__argc`/`__argv` CRT globals instead of changing `main`'s ABI, and an empty slice under JIT

**Decision:** the predeclared `args() []string` builtin does **not** read
real argc/argv through `main`'s own parameters (`main`'s LLVM signature stays
the exact same parameterless `i32 @main()` it always was - see the `-o` entry
above for why changing it was rejected). Instead, `buildArgsInitFn` reads two
plain extern globals, `__argc`/`__argv` - the same well-established
MSVCRT/mingw64 C-runtime extension a real, hand-written C/C++ program on this
platform already relies on, populated by the CRT's own startup sequence
before `@llvm.global_ctors` or `main` itself ever run - and marshals them
into a freshly arena-allocated `[]string`, stored into a private
`llvm_lang.args` global once, via a synthesized ctor function
(`llvm_lang.args_init`) registered into `@llvm.global_ctors` at a lower
priority than `llvm_lang.global_init` (so it runs first - a non-constant
global's own initializer might itself call `args()`).

**Built (and its own `__argc`/`__argv`/`strlen` externs declared) only for a
program that actually calls `args()` somewhere** (`Generator.argsUsed`) - not
unconditionally for every module the way `printf`/`malloc`/etc. already are.
This forced `genPackage`'s own final pass (`genCtors`) to move from running
*before* every function/constructor/destructor body is generated to running
*after* - `g.argsUsed`'s final value isn't known for certain until every body
has already been generated. This reordering changes nothing about
correctness, confirmed by this project's full existing test suite passing
unchanged.

**Why not build this unconditionally, the way every other runtime extern
already is?** `__argc`/`__argv` are real external symbols this package has no
control over the resolvability of under JIT execution - genuinely unlike
`malloc`/`printf`/`memcpy`/etc., already proven resolvable by this entire
project's existing test suite. Keeping them out of every program's module
unless that program actually calls `args()` means the vast majority of
existing and future programs carry zero new external-symbol risk at all.
`bindMinGWMainThunk` additionally binds both to harmless, always-valid
process-local memory under JIT via the same `AbsoluteSymbols` mechanism
already used for the unrelated `__main` MinGW/GCC ABI quirk - confirmed
directly by `TestArgsCallUnderJITReturnsEmptySlice` et al. actually
JIT-executing successfully.

**Why an empty slice under JIT, not real trailing-argument forwarding
through `llvmc`.** The alternative considered - `llvmc` accepting trailing
positional arguments and threading them through the JIT's own raw-syscall
`main` invocation - was rejected for two compounding reasons: (1) it would
need `main`'s own LLVM signature to carry real argc/argv parameters after
all, exactly the regression risk already rejected above; (2) even granting
that, correctly poking an already-marshaled `{ptr, i32, i32}` slice value
directly into a live JIT'd module's global memory from the Go host process
would require this driver to independently reconstruct LLVM's own exact
struct layout/alignment rules by hand, entirely outside any actual generated
code - a fragile, easy-to-get-subtly-wrong approach for a fallback path this
project's own explicit instructions permitted skipping. An empty slice is
simple, cannot ever be subtly wrong, and is clearly documented rather than
silently different behavior a user could stumble into unwarned.

**Status:** shipped. See `LANGUAGE.md`'s "The `args()` builtin" section for
the full language-level rule (including the JIT-vs-AOT behavioral difference,
called out explicitly) and `CODEGEN.md`'s own section of the same name for
the lowering; `src/codegen/args_test.go`/`cmd/llvmc/main_test.go`'s
`TestBinary_AOT_Args` for the JIT-empty-slice vs. AOT-real-argv contrast, and
`TestArgsUnusedProgramHasNoArgsMachinery`/`TestArgsUsedProgramHasArgsMachinery`
for the conditional-machinery behavior this entry describes.

---

## 2026-07-23 - Runtime trap diagnostics: an informative message, unchanged hard-abort mechanism

**Decision:** every runtime safety trap this project already had
(`genBoundsCheck`/`genSliceRangeCheck`; `genMakeSizeCheck`) now prints a
real, informative `printf`-based message - the actual runtime values
involved (an out-of-range index and the size it was checked against; a bad
slice's low/high/capacity; make's own len/cap) - immediately before the
existing `llvm.trap` + `unreachable` sequence. The abort mechanism itself is
completely unchanged: still a genuine illegal-instruction process crash, not
a graceful `exit(1)` or any kind of recoverable panic/exception - this
language still has no exception-handling concept anywhere.

**Why:** a bare `llvm.trap` with zero diagnostic output made debugging a real
program's out-of-bounds/bad-slice/bad-make failure needlessly painful -
nothing distinguished *which* check failed or *what* the actual bad values
were short of attaching a debugger. Go's own runtime panic convention (a
message, then a hard crash) was the explicit model, deliberately not a
softer recovery mechanism. Reusing the exact same `printf`/cached-format-
string mechanism `print`'s own codegen already established meant this needed
no new runtime primitive at all - three more format-string globals in the
same table every other cached format string already lives in.

**Status:** shipped. See `CODEGEN.md`'s new "Runtime trap diagnostics"
section for the exact message text/values per site, and
`TestOutOfBoundsIndexTraps`/`TestSliceRangeCheckTraps`/
`TestMakeCapLessThanLenTraps`/`TestMakeNegativeSizeTraps` for the
printed-message assertions added on top of each test's existing
abnormal-exit assertion.

---

## 2026-07-23 - Go-style multi-return values: destructuring only, no tuple type

**Decision:** add Go-style multi-return values - `func f() (T1, T2, ...)`,
`a, b := f()` - confirmed directly with the project owner as this language's
answer to error handling, now that a fallible function has a real way to
signal failure besides a sentinel value or an out-pointer parameter. Scoped
deliberately narrowly, mirroring Go's own actual restriction: **there is no
first-class tuple type** - a multi-return call's result can only ever be
consumed by destructuring it immediately, at the exact point a matching call
happens (`a, b := f(...)`/`a, b = f(...)`) - it can never be stored in a
single variable, passed onward as one value, or used any other way. Every
other position (a single-name `:=`, a call argument, `print`, an operator
operand, ...) rejects a multi-return result outright, with a real
diagnostic.

**Why (destructuring only, no tuple type):** a real tuple type would be a
substantially larger feature - a new storable `Type` needing its own
assignability/equality/composite-literal rules, interacting with every
existing construct that can hold an ordinary value - none of which the
motivating error-handling use case (`v, ok := f(...)`) actually needs. Go
itself draws the identical line for the identical reason. Modeled as
`Type{Kind: TypeMultiReturn, Params: []Type}` (reusing `TypeFunc`'s own
`Params []Type` field) and rejecting it everywhere except the two consuming
positions was the smallest change fully supporting the real use case.

**Why new node kinds, not retrofitted existing ones:** `ReturnStmt` stays
fixed-arity `[expr]` unchanged - a multi-value return wraps its values in a
new variable-arity `MultiValueExpr` node in that same slot, mirroring
`ParamList`'s own established "variable-arity part gets its own wrapper
node" precedent. Likewise a new `MultiReturnType` wrapper and genuinely new
`MultiShortVarDecl`/`MultiAssignStmt` node kinds - following the same
reasoning `ConstructorDecl`/`DestructorDecl`/`ExternFuncDecl` already
established: every single-value call site elsewhere assumes exactly one
value/name/target, and retrofitting those shapes would have rippled a
nil-check through all of that for zero benefit.

**Why each destructured name's own type is eagerly cached, not left lazy:** a
`MultiShortVarDecl`-declared name's own component type is computed once,
directly against that name's own `Ident` node - both `declType`'s
memoization cache and `info.Types` are seeded unconditionally, rather than
relying on `declType` being invoked lazily. A destructured name that's
declared but never referenced again would otherwise never have its own
`info.Types` entry populated - codegen's own lookup would silently read the
`Type{}` zero value. Ordinary `ShortVarDecl` doesn't have this gap only
because `checkStmt` always forces `declType` on the *whole* declaration node
directly.

**Why the codegen ABI needed nothing new at all:** a multi-return function's
real LLVM signature returns an anonymous struct `{T1, T2, ...}` - exactly the
same "aggregate passed and returned as a real LLVM type, no manual sret" this
project's structs already use, just for an anonymous aggregate. `return a, b`
builds it via `llvm.Undef` + `CreateInsertValue`; destructuring reads it back
via `CreateExtractValue`. No new LLVM type needed caching in `setupTypes`
either - unlike `stringTy`/`dynArrTy`/`funcValTy` (one fixed shape reused
everywhere), every multi-return function's own component types differ, so
each one's anonymous struct type is simply built fresh, on demand.

**Explicitly out of scope, confirmed directly rather than assumed worth
adding:**

- **General Go-style parallel multi-assignment** (`a, b := 1, 2`) - a
  genuinely different, larger feature than destructuring one multi-return
  call. This language's destructuring grammar only ever parses a single
  expression as the right-hand side, so this form isn't reachable through the
  grammar at all - a leftover comma-separated value is simply an ordinary
  syntax error.
- **Argument-spreading** (Go's own `f(g())`). Every argument position is an
  ordinary single-value context, so this is rejected the same way any other
  single-value position rejects a multi-return result.
- **A blank identifier (`_`)** for discarding one of several destructured
  values. This language has no blank-identifier concept anywhere yet - every
  destructured value must bind to (or assign into) a real, distinctly-named
  target this round. Documented in `LANGUAGE.md` as a deliberate,
  likely-worth-revisiting-later gap.

**Status:** shipped. See `LANGUAGE.md`'s "Functions" section for the full
language-level rule and `CODEGEN.md`'s new "Go-style multi-return values"
section for the lowering. New coverage across `src/parser`/`src/sema`/
`src/codegen` (`multireturn_test.go` in each), plus a real worked example
(`examples/multireturn/multireturn.llx`) exercised end to end - JIT and AOT
alike - by `cmd/llvmc/main_test.go`.

---

## 2026-07-23 - Maps: open addressing + tombstones, a word-wise FNV-1a-style hash, `remove` over reusing `delete`

**Decision:** `map[K]V` is a real hash table, backed by the same arena
allocator every other heap-needing feature already routes through - open
addressing with linear probing and tombstone-marked deletions (not separate
chaining), a word-wise FNV-1a-*style* recursive hash combinator (not a
literal byte-for-byte FNV-1a pass over a key's raw memory), doubling growth
at a 0.75 load factor, and a brand-new predeclared `remove(m, k)` builtin for
key removal rather than reusing the existing `delete p` statement. Scoped to
storage/lookup/removal only - no `range`-style iteration, no
`keys(m)`/`values(m)` helpers, no map composite-literal syntax.

**Why open addressing + tombstones over separate chaining:** genuinely
simpler to implement correctly and reason about - one flat, arena-allocated
bucket array per map, no per-entry pointer indirection/chaining links to
allocate or reason about aliasing for. The one real wrinkle - a naive "clear
the slot on delete" would break a probe sequence that legitimately continues
past a deleted slot to reach a still-live key further along - is solved the
standard way: a distinct tombstone tag, never treated as "probe stops here"
the way a genuine empty slot is, but still eligible to be reused as an
insertion point.

**Why a word-wise recursive hash combinator over literal byte-for-byte
FNV-1a:** this project's own struct/array *values* are real LLVM aggregates
built via `InsertValue`, with no guarantee their own inter-field padding
bytes are ever deterministically zeroed - hashing "every raw byte the LLVM
type occupies" risks hashing two logically-identical struct values to two
different results purely from garbage bits in padding, silently breaking the
one property a hash table can't survive without (equal keys MUST hash
equal). Recursing through a key's own *logical* structure - each numeric
field/element's own bit pattern, a string's own real content bytes, walked
recursively for a nested struct/array - and mixing only those bits with the
same simple FNV-1a-style `seed = (seed XOR word) * 16777619` fold sidesteps
the padding hazard entirely, while staying exactly as simple a mixing
function as literal FNV-1a itself. See `CODEGEN.md`'s "Maps" section for the
full worked rationale.

**Why `remove(m, k)` as a genuinely new builtin, not an extension of `delete
p`:** `delete p` is a real, unrelated operation - heap pointer deallocation -
operating on a completely different kind of value for a completely different
reason. Overloading that one keyword would be a confusing collision between
two unrelated concepts sharing only surface-level vocabulary; a clean,
distinctly-named `remove(m, k)` builtin (the same predeclared-function
mechanism `make`/`append`/`len`/`args` already use) avoids the collision
entirely and needs no new grammar at all.

**Why map iteration (`for k, v := range m`) was left out entirely, not just
narrowly deferred:** this language has **no `range`-style for-loop grammar at
all yet**, for anything - only the three plain C-style `for` forms exist.
Inventing `range` just for maps, when nothing else in the language has it
either, is a substantially bigger, genuinely separate feature - not
something to back into narrowly scoped to maps alone. `keys(m)`/`values(m)`
helpers were considered but likewise deferred, since they weren't needed to
make the feature's own worked example (`examples/word_freq/word_freq.llx`)
complete.

**Why a map composite literal (`map[string]int{"a": 1}`) was left out:** Go
has this, but it's a real, separate grammar extension - this language's
existing `CompositeLit` machinery is built specifically around struct/array
shapes, not a `key: value` *pair* list keyed by arbitrary expressions.
`make(map[K]V)` plus individual `m[k] = v` insertions was judged sufficient
for this round; writing `map[...]...{...}` today is simply a plain parse
error, not a silently-mishandled case.

**Status:** shipped. See `LANGUAGE.md`'s new "Maps" section for the full
language-level rule (key-comparability restriction, the `v, ok := m[k]`
two-result index expression and its precise distinction from a real
multi-return call, every explicit scope boundary above) and `CODEGEN.md`'s
new "Maps" section for the hash table's exact representation/growth/
collision-resolution scheme. New coverage across `src/parser`/`src/sema`/
`src/codegen` (`map_test.go` in each - grammar, key-comparability, mutation
restrictions, growth/rehash forced by 50 distinct keys, struct-typed keys
colliding by structural value, nested maps), plus a real worked example
(`examples/word_freq/word_freq.llx` - a word-frequency counter over
`std/strings.Split`) exercised end to end - JIT and AOT alike, both producing
byte-identical, hand-verified output.

---

## 2026-07-23 - `==`/`!=` and `print` gain a real comparability/printability gate in sema

**Decision:** `checkEqualityOperands`'s struct/array branch now runs a new
`typeIsComparable` predicate over the whole aggregate type, alongside its
existing `lt.Equal(rt)` check, before admitting `==`/`!=` between two
same-typed structs/arrays. `typeIsComparable` is `typeIsComparableKeyType`
(originally written for `map[K]V`'s own key-type restriction) generalized
and renamed - the exact same recursive "walk every field/element, reject a
dynamic array/function type/map type anywhere nested inside" logic, now
shared by both the map-key-declaration site and this operator.
`checkPrintCall` gets its own, separate `typeIsPrintable` predicate, gating
`print`'s single argument the same recursive way.

**Why (root cause, not another codegen patch):** a 5-agent code review found
`checkEqualityOperands` accepted `==`/`!=` between two same-typed
structs/arrays via a bare whole-type `Type.Equal` check, never validating
that every recursively-nested field/element `Kind` was actually something
codegen's `genValueEqual` could lower. Two distinct failure modes followed:
a struct field of `TypeMap` or `TypeFunc` reached `genValueEqual`'s
`default:` case and panicked the whole compiler; far worse, a struct field of
a *dynamic array* (`[]T`) reached the `TypeArray` case, whose `for i := 0; i
< int(t.Size); i++` loop runs *zero times* for a dynamic array (`t.Size` is
never set when `Dynamic` is true) - silently comparing that field as
always-equal regardless of its real length/contents, so `a == b` for two
structs differing only in a slice field's contents evaluated to `true`.
`checkPrintCall` had the identical shape of bug for a different reason: it
accepted "exactly one argument, of any type" with zero restriction, while
codegen's `genPrintCall`/`genPrintValueBare` only ever implemented a fixed
set of `Kind`s and panicked on a bare function value, map value, or either
nested inside a struct field. In both cases the reactive fix - just widening
codegen's switch again - was rejected: `genValueEqual` had already been
widened once, that same day, on a doc comment claiming its switch "must
cover every Kind a struct field or array element can legitimately have" - a
claim that was never actually true. The real, load-bearing fix belongs in
the layer that decides what's legal in the first place - sema - restoring
codegen's own "unreachable given an already-checked tree" invariant instead
of chasing the symptom in codegen a second time.

**Why two separate predicates, not one shared one:** `typeIsComparable` and
`typeIsPrintable` are deliberately *not* the same allowlist, despite sharing
almost all of their logic. A dynamic array is printable (`genPrintArrayValue`
already renders one correctly, an existing, tested feature that must not
regress) but is never comparable (`==`/`!=` already rejected a bare slice
outright, for the same "no meaningful equality" reason a map/func value is
rejected). Reusing one predicate for both call sites would have either
wrongly allowed comparing two slices or wrongly rejected printing one;
keeping them as two small, separately-named functions makes that difference
an explicit, checkable invariant instead of an implicit coincidence.

**Also landed alongside this:** real `TypePointer` support in codegen's
printing - a pointer was already comparable and is included in the new
printable set too, but codegen had no case for it at all until now. It
prints via a new `"%p\n"`/`"%p"` format-string pair, the same
"declare-a-libc-printf-format-string" convention every other `fmtInt`/
`fmtFloat`/`fmtStr` pair already follows - no new runtime primitive needed.

**Status:** shipped. Both failure modes (the map/func-field panic, and the
dynamic-array-field silent-`true` bug) now produce a clean compile-time
diagnostic instead. See `LANGUAGE.md`'s Operators section and its `print`
builtin section for the user-facing rule, and `CODEGEN.md`'s "`print`
builtin, concretely" section for the pointer-printing addition.

---

## 2026-07-23 - Default-on LLVM `default<O2>` optimization pipeline, with a `-no-opt` escape hatch

**Decision:** `src/compiler`'s `finishPipeline` now runs LLVM's real
`default<O2>` pass pipeline (`llvm.Module.RunPasses`, the vendored
`third_party/go-llvm`'s `passes.go`) over every module right after
`llvm.VerifyModule` succeeds, in the one shared pipeline tail every
consumption path (JIT execution, `-emit-llvm`, `-o`) already funnels
through - not duplicated per path. `CompilePackage`/`CompileProgram`/
`finishPipeline` all gained an explicit `optimize bool` parameter (this
project's own established style over a hidden default or functional
options); every existing call site now passes `true` except `cmd/llvmc`'s new
`-no-opt` CLI flag, which threads `false` through. `finishPipeline` also now
builds this host's own `llvm.TargetMachine` once, unconditionally, and
exposes it via a new `Result.TargetMachine` field - `cmd/llvmc`'s
`compileToExecutable` (the `-o` AOT tail) now reuses that instead of
building a second, separate `TargetMachine` of its own.

**Why now:** discovered while benchmarking llvm_lang against Go/Node.js - a
trivial 100M-iteration arithmetic loop ran ~3x slower than equivalent Go/JS
code, and reading `src/compiler/compiler.go` directly confirmed why: this
compiler ran **zero LLVM optimization passes**, ever, at any stage.

**Why `default<O2>`, not `O1`/`O3`/`Os`/`Oz`:** `O2` is LLVM's own standard,
well-balanced pipeline - the same one `clang -O2` runs - giving real
inlining/mem2reg/GVN/LICM/DCE without `O3`'s more aggressive, occasionally
UB-exploiting/code-size-inflating tradeoffs, and without `Os`/`Oz` trading
away runtime speed for code size (not this project's goal).

**Why on by default, with a disable flag, not opt-in:** every real consumer
of this compiler wants fast code by default - matching how every mainstream
compiler (`clang`, `go build`, `rustc`) treats *some* optimization as the
ordinary, expected case. `-no-opt` exists specifically for debugging:
comparing its output against the default optimized output tells you whether
a bug lives in codegen's own lowering or was introduced by an optimization
pass - `-no-opt` skips `RunPasses` entirely, a genuine, byte-for-byte
restoration of this compiler's pre-this-round behavior.

**Why the `TargetMachine` moved into `src/compiler` and got shared, not left
duplicated:** `cmd/llvmc/main.go`'s `-o` tail already built its own
`TargetMachine` from scratch purely for its own object-code emission.
`RunPasses` needs one too, so building it once in `finishPipeline` and
handing it back via `Result` removes the duplication instead of adding a
second copy of the same three calls. Disposal is caller-owned, exactly like
`Result.Module` already is.

**A real regression this round's own verification caught, not shipped:**
turning on `default<O2>` broke dynamic array/struct printing - every literal
punctuation character `genPrintLiteral` prints (`[`, `]`, ` `, the trailing
newline) vanished from JIT/AOT output, because LLVM's `SimplifyLibCalls`
recognizes `printf` called with a constant, single-character,
no-format-specifier format string and rewrites it into a bare `putchar`
call - not a safe rewrite under this project's own JIT hosting, where
`putchar`'s and `printf`'s underlying stdio buffers can't be assumed to be
the same one. Fixed by routing every `printf` call this package emits
through one new choke point, `callPrintf`, which tags the call site with
LLVM's `nobuiltin` attribute (the same attribute Clang emits under
`-fno-builtin`) - telling the optimizer never to recognize these particular
calls as the corresponding libc built-in, without disabling `printf`/libc
recognition globally. Caught by this round's own full-example regression
sweep (optimized vs. `-no-opt` output diffed for every program under
`examples/`) before being considered done.

**Status:** shipped. See `CODEGEN.md`'s new "Optimization pipeline" section
for the full design (including the `nobuiltin` fix) and `BENCHMARKS.md`'s
dated entry for the resulting `CompilePackage` end-to-end cost increase.

---

## 2026-07-23 - Arena allocator: geometric (doubling) chunk growth, not a fixed 64KiB every time

**Decision:** replace `setupArena`'s fixed-size growth (every chunk after the
first was another flat `arenaChunkSize`, 64KiB, forever) with geometric
growth: a new mutable global, `.arena.next_chunk_size`, starts at
`arenaChunkSize` and doubles on every *ordinary* (non-oversized) growth
event, capped at a new `arenaChunkMaxSize` (64MiB). The starting chunk size
itself is unchanged. An oversized one-off request (bigger than the current
tracked chunk size) is still served at exactly its own size and deliberately
does **not** advance `.arena.next_chunk_size` - see `CODEGEN.md`'s "The arena
allocator" section for the full mechanics.

**Why:** empirically root-caused, not theorized. A 50,000-iteration `s = s +
"x"` loop (the classic O(n^2) naive-immutable-string-concat pattern) was
running about 2.8x slower than Go's own equally-naive `+=` on the identical
workload. Traced precisely: this benchmark's *cumulative* allocation volume
is roughly 1.25GB over the whole loop (the sum of every intermediate
string's own size). At a fixed 64KiB chunk size, that's about 19,000 real
`malloc` calls over the loop - confirmed directly by temporarily hardcoding
`arenaChunkSize` to 16MiB (~683ms -> ~400ms) and then to 128MiB (~313-344ms,
closing most of the remaining gap to Go's own ~243ms baseline), then
reverting both experimental changes before implementing the real, permanent
fix. The fixed small chunk size - not `memcpy` throughput, not codegen
quality - was the dominant cost.

**Why 64MiB specifically (not 16MiB or 128MiB):** the two manual experiments
above bracket a real tradeoff. 128MiB gets closer to Go's own baseline, but
means a long-running, allocation-heavy program keeps requesting genuinely
large single blocks well past the point of real per-`malloc`-overhead
benefit, with no way to reclaim an abandoned block's unused tail. 64MiB sits
between the two experimental data points, capturing most of the realistic
win (post-fix, the same benchmark measured 355-474ms across repeated min-of-5
runs, comfortably inside the 313-442ms range the manual experiments found)
without reserving as aggressively as 128MiB would for a workload with no
guarantee it will ever need that much.

**Why an oversized one-off deliberately doesn't touch the tracked
baseline:** a single unusually large allocation would otherwise permanently
inflate every *later, ordinary* chunk for the rest of the program's run, even
though its own steady-state allocation pattern never asked for anything that
large again. Keeping "ordinary, tracked-and-doubling" vs. "oversized,
served-once-and-forgotten" genuinely separate avoids that.

**Verification given the severity class of a bug here** (silent heap
corruption in the one allocator every heap-needing language feature routes
through): beyond the standard test sweep and a byte-identical regression
diff across every example under `examples/`, a dedicated correctness stress
suite (`src/codegen/arena_growth_test.go`) specifically walks the *entire*
geometric progression via many small independent allocations, builds one
genuinely large (128MiB) single string via doubling with byte-exact content
checks at start/middle/end, interleaves small string concatenations with
large dynamic-array append bursts, appends 3,000,000 elements to a single
dynamic array (verified via an `i64` closed-form checksum), and churns a
20,000+5,000-key map through heavy insert/remove/insert cycles - each
deliberately checking real per-element/per-byte content, not just a final
length or count. One of these tests was confirmed to actually fail (a real
process crash) when a deliberate bug was temporarily reintroduced into the
grow path's `needsBigger` comparison, before being reverted - confirming the
suite doesn't just pass vacuously.

**Status:** shipped. See `CODEGEN.md`'s "The arena allocator" section for the
updated design and `src/codegen/runtime.go`'s `arenaChunkSize`/
`arenaChunkMaxSize`/`setupArena` doc comments for the exact mechanics.

---

## 2026-07-23 - Rust-style enums + `match`: comprehensive from day one, `{i32, ptr}` representation, statement-only/enum-only scope for `match`

**Decision:** build the full feature in one round rather than a narrower
first cut - unit/tuple/struct variants, methods, destructors with the exact
same non-copyable propagation rule structs already have, recursive/self-
referential variants, `==`/`!=`/`print()` with a real runtime discriminant
dispatch, and an exhaustive `match` statement - all landing together.

**Why comprehensive, not narrow, this time:** every previous feature in this
project's history started narrow specifically because there wasn't much
existing language surface for it to sit consistently alongside yet. Enums
are the opposite case: by the time this round landed, the project already
had a mature, load-bearing precedent for every piece this feature needed to
reuse - a real non-copyable/destructor system (structs), a real
keyed-composite-literal grammar (structs), a real receiver-method mechanism
(structs), and a real arena-allocation idiom for "small fixed header, real
payload on the heap" (dynamic arrays, closures, maps). Landing enums narrow
and adding struct-style variants/destructors/exhaustiveness in a *third*
round later would have meant redoing the same integration work twice for no
real benefit.

**Why variant construction needs no separate `constructor(){}` block (unlike
a struct):** a struct constructor exists specifically to run custom logic a
bare composite literal can't - a struct's own composite literal is
deliberately "raw structural construction, bypassing constructors entirely."
A variant's own construction (unit/tuple/struct-literal) *is* the value -
there is no second, "raw" way to build the identical value that a
constructor could meaningfully differ from.

**Why `{i32, ptr}` - one shared LLVM type for every enum - rather than a
named per-enum struct sized to its largest variant:** the natural
alternative, `{i32, [N x i8]}` with `N` = the largest variant's own byte
size, needs `N` as a real Go integer at struct-type-construction time. This
project's existing `llvm.SizeOf`-based sizing idiom is a lazy LLVM constant
expression, only ever resolved once the module is compiled/JIT'd - getting a
real Go-side integer out of it before that point would need a genuine
`llvm.TargetData` threaded through this package for the first time, a new
dependency purely to serve one type kind's own struct layout. The `{i32,
ptr}` shape needs none of that, and is exactly this project's own
already-established idiom elsewhere - reusing it here cost nothing new to
build and made every genuinely hard case (a recursive/self-referential
variant, an enum-of-enum field) fall out for free with zero special-casing:
a pointer is always just `g.ptrTy` regardless of what it points to. The real
cost - every non-unit variant construction is a real arena allocation, never
a stack value - is judged an acceptable, well-precedented tradeoff, since
every comparable "small header + heap payload" value in this project already
pays the identical cost.

**Why `match` is a statement, not an expression, this round:** the
motivating use case (dispatching on an enum's active variant, running
different logic per case) doesn't need a value handed back to an enclosing
expression - every arm in the worked example already `return`s directly.
Building `match` as an expression too would mean deciding a whole second set
of rules this round didn't actually need answered yet (every arm's result
type must unify to one common type; what a non-exhaustive match-expression
even means) - a real, separable feature addition for a later round. Go
itself draws an analogous line (`switch` is a statement, not an expression)
for the same underlying reason.

**Why `match` is scoped to enum-variant patterns only, not a general
value-switch:** the exhaustiveness check - genuinely the entire value
proposition of building `match` as its own construct rather than an
unchecked switch - only has real, checkable meaning against a *closed* set
(an enum's own declared variants). A general `switch` over arbitrary values
has no such closed set to check exhaustiveness against at all (Go's own
`switch` has no exhaustiveness checking for exactly this reason) -
conflating the two into one construct this round would have diluted the one
thing that makes enum-variant `match` worth building in the first place. The
general value-switch capability remains a clean, separable extension,
deferred to a later round rather than an oversight here.

**Status:** shipped. See `LANGUAGE.md`'s "Enums"/"match" sections for the
full language-level rules (including both deferred-to-a-later-round
boundaries called out above, documented there as deliberate scope, not gaps)
and `CODEGEN.md`'s "Enums" section for the representation and codegen this
entry describes.

## 2026-07-23 - `match` generalized into a general value-switch: mandatory wildcard, float exclusion, enum arms stay single-pattern

**Decision:** deliver the value-switch capability the previous entry
explicitly deferred: `match` now also accepts a plain `i8`/`i16`/`i32`/
`i64`/`bool`/`string` subject, Go-`switch`-style, dispatching on ordinary
value equality rather than only an enum's own declared variants. Each arm's
pattern list is generalized from a fixed single pattern to a
comma-separated list of one or more (Go's own `case a, b, c:` shape), shared
by both the enum-match and value-match paths at the grammar level - sema is
what actually restricts an enum-match arm back down to exactly one pattern.
The two subject kinds get genuinely different type-checking
(`checkEnumMatchStmt` vs. `checkValueMatchStmt`) and codegen (`genMatchStmt`'s
existing LLVM `switch` vs. the new `genValueMatchStmt`'s runtime comparison
chain) - see `CODEGEN.md`'s "match codegen" section for the full lowering
breakdown - dispatched purely by the subject's own resolved type.

**Why a mandatory wildcard, where Go's own `switch` doesn't require one:**
the previous entry's exhaustiveness reasoning still holds exactly as
written - an unbounded domain like `int`/`string` has no closed set to check
coverage against, so a *real* exhaustiveness check is simply impossible
here, full stop. Go's own answer is to not require anything at all: a
`switch` with no matching case and no `default` just silently does nothing.
This project deliberately takes the stricter position instead: `match`
exists specifically to be a real safety net, and a value-match that can
silently no-op on an uncovered case would quietly undermine that for exactly
the inputs a programmer is least likely to have tested. Requiring a
wildcard converts "did I forget a case" from a silent runtime gap into a
compile-time question the language actually answers, even though it can't
tell *which* cases you forgot the way the enum path can name uncovered
variants by name.

**Why `f32`/`f64` are excluded as a value-match subject type:** float
equality is already a known footgun this language deliberately doesn't lean
into anywhere else - `==`/`!=` on floats works (IEEE `OEQ`/`UNE`, NaN handled
per spec), but nothing in this language *encourages* branching on float
equality the way a value-match subject would. Excluding it outright - a
clean diagnostic, not a silently-accepted footgun - costs nothing real:
nobody was relying on float value-matching before this round existed at
all, and an ordinary `if`/`else if` chain with an explicit tolerance
comparison remains the correct tool for float-based branching regardless.

**Why an enum-match arm stays restricted to exactly one variant pattern,
even though the grammar itself now allows several:** binding several
differently-shaped variant patterns into one shared arm body is a real,
separate feature question - each pattern binds different names to different
types (or, for a mix including a unit variant, no names at all). Go itself
has no equivalent to reach for here, so there's no existing precedent to
lean on for how the bindings should unify, if at all. Rather than inventing
and shipping that design half-considered alongside the (unrelated)
value-switch feature this round was actually asked to deliver, an
enum-match arm with more than one pattern is simply rejected with a clean
diagnostic - the identical "ship the narrower, correct thing now" reasoning
the previous `match` entry itself used for scoping the feature to
enum-variant patterns in the first place.

**Status:** shipped. See `LANGUAGE.md`'s "match" section ("Enum matching",
"Value matching", and the updated "Explicitly deferred" list) and
`CODEGEN.md`'s "match" sections for the resolution/check/codegen split this
entry describes.

---

## 2026-07-24 - General Go-style parallel multi-assignment: reusing `MultiValueExpr`, evaluate-all-then-assign-all

**Decision:** ship the one piece the original multi-return-values round
explicitly scoped out - `a, b := 1, 2` and `a, b = 1, 2`, each side
individually evaluated and paired positionally, with no multi-return call or
map index involved at all. The grammar change is minimal:
`finishMultiShortVarDecl`/`finishMultiAssignStmt` now check for a trailing
comma after the first parsed value, exactly the way `parseReturnStmt`'s own
multi-value `return a, b, ...` already does - collecting the rest and
wrapping *all* of them (including the first) in a `MultiValueExpr` node, the
destructuring statement's existing trailing "value" slot. No comma at all
leaves that slot exactly as it was before this round.

**Why reuse `MultiValueExpr` rather than a new node kind:** it already means
"a variable-arity, comma-separated list of independent value expressions
sitting in a fixed slot that used to hold exactly one expression" - literally
the shape this feature also needs, just in a different fixed slot. Inventing
a second node kind for the identical shape would only exist to distinguish
"this comma list came from a `return`" vs. "this comma list came from a
destructuring statement" - a distinction already implicit in which parent
node the `MultiValueExpr` sits under, so a new kind would carry zero extra
information sema/codegen actually need.

**Why sema defaults each value independently, right inside
`checkDestructureSource`, rather than deferring to the caller:** the new
`MultiValueExpr` branch there checks (and, where untyped, defaults) every
value via `checkValueExpr`/`defaultIfUntyped` - the exact same per-position
defaulting a plain single-value `x := expr` already gets - and returns
already-concrete types, mirroring `checkMultiValueReturn`'s own per-value
loop one layer up. This was a genuine design choice: `checkMultiAssignStmt`'s
own later `checkAssignable` call against each *existing* target's type could
in principle have been left to adapt an untyped value directly, the way a
plain single-target `x = 5` already lets an untyped literal adapt to `x`'s
own declared type. Defaulting eagerly instead keeps every value position's
own type independent and decided in exactly one place - at the cost of a
narrow, deliberate asymmetry with plain single-target assignment: `var x f64;
a, x = 1, 5` types `5` as `int` (not adapted to `x`'s own `f64`) before ever
comparing it against `x`. Not exercised by anything in this round's own
worked example; worth revisiting if a real program ever needs it.

**Why evaluation order needed real codegen attention (the swap idiom):**
`genMultiShortVarDecl`/`genMultiAssignStmt` evaluate every `MultiValueExpr`
child into its own temporary SSA value first, in source order, *before* any
store happens - for `genMultiAssignStmt` this means every target's address
is computed, then every value is read, then every store runs. This is what
makes `a, b = b, a` a genuine swap: both `b` and `a` are read at their
pre-assignment values before either target is overwritten, matching Go's own
real assignment semantics exactly (Go's own spec calls this out explicitly).
Getting this ordering wrong wouldn't crash or produce an obviously-broken
result - it would silently produce a plausible-looking wrong answer (`a, b =
b, a` degenerating into `a = b; b = a`), which is exactly why this needed a
dedicated codegen test proving the swap (`TestParallelMultiAssignSwap`) and a
real 3-way rotation (`TestParallelMultiAssignThreeWayRotation`), not just
"it built, it ran, the values looked right."

**Status:** shipped. See `LANGUAGE.md`'s "Go-style multi-return values"
section's new "General Go-style parallel multi-assignment" subsection for
the full language-level rule (including the still-deferred argument-
spreading/no-tuple-type boundaries, unchanged from the original round). New
coverage across `src/parser`/`src/sema`/`src/codegen` (`multireturn_test.go`
- `MultiValueExpr` shape on both statement forms including the swap idiom,
independent per-position typing/defaulting, every count-mismatch rejection,
plain parallel init, and a 3-way rotation), plus a real worked example
(`examples/multi_assign/multi_assign.llx`) exercised end to end - JIT and AOT
alike - by `cmd/llvmc/main_test.go`.

---

## 2026-07-24 - `match` as an expression: a distinct `yield` keyword, and a bare-expression arm desugars to an implicit one

**Why a new `yield` keyword instead of reusing `return`:** the motivating
design conversation surfaced a real footgun directly - a match-expression
arm's block can contain ordinary logic (`if`, loops, ...) before producing
its value, and the natural instinct is to write `return "small"` to mean
"this is the arm's value." But `return` already has one fixed meaning
everywhere else in this language: exit the *whole enclosing function*.
Overloading it to *also* mean "produce this match arm's value, don't exit the
function" depending on whether it happens to sit inside a match-expression
arm would make the same keyword do two different things based on unlabeled
lexical context. A distinct `yield` keyword removes the ambiguity entirely:
`return` inside a match-expression arm still means exactly what it always
has (exit the function; that path needs no `yield` of its own), and `yield`
means exactly one thing (produce this arm's value, exit just this match
expression).

**Why a bare-expression arm (`pattern => expr`) desugars to an implicit `{
yield expr }` at parse time, rather than sema/codegen handling two arm-body
shapes:** every downstream consumer (`checkMatchExprArmBody`'s "every path
yields" flow analysis, `genMatchExpr`'s phi-value collection) only ever needs
to reason about ONE canonical shape - a `Block` whose every reachable path
ends in a real `YieldStmt` - regardless of which surface form the user
actually wrote. Handling "sometimes a bare expression, sometimes a block" as
two parallel code paths in sema and codegen would have meant duplicating the
flow-analysis and value-collection logic for no real benefit; the
desugaring is invisible past the parser.

**Why `yield`'s own destructor unwind targets the match expression's own
entry depth, not the whole function's:** mirrors `break`/`continue`'s own
existing `destructorBase` precedent exactly - a `yield` only unwinds locals
declared since *this* match expression started, never anything declared in
an enclosing scope outside it. Getting this wrong in either direction is a
real correctness bug: unwinding too far double-destructs an outer local
still very much alive; not unwinding far enough leaks a local's destructor
call entirely.

**Why the enum/value dispatch and exhaustiveness logic (`checkMatchDispatch`,
`matchArmsAllTerminate`) are shared, not duplicated, between statement- and
expression-mode match:** both modes need the identical "is the subject an
enum or a value type, is it exhaustive, is a variant matched twice" checks -
the *only* things that differ are (1) how each arm's own body gets checked
and (2) what happens with the result afterward. Both are threaded through as
a `checkArm`/`armTerminates` callback parameter rather than re-implementing
the whole dispatch a second time - the same technique
`matchStmtTerminates`/`mustYieldEveryPath` share, and `genMatchStmt`/
`genValueMatchStmt` share via a nullable `frame` parameter on the codegen
side.

**Status:** shipped. See `LANGUAGE.md`'s "match" section's new "`match` as
an expression" subsection for the full language-level rule, and
`CODEGEN.md`'s "`match` as an expression" subsection for the
`frame`/`matchExprCodegenCtx`/phi-construction codegen shape this entry
describes. New coverage across `src/parser`, `src/sema`, and `src/codegen`
(`matchexpr_test.go` in each), plus a real worked example
(`examples/match_expr/match_expr.llx`, both arm-body shapes freely mixed)
exercised end to end - JIT and AOT alike.

---

## 2026-07-24 - `range` scoped to maps and arrays only, hardcoded rather than a general iterator protocol

**Decision:** add `for [key[, value]] := range subject { ... }` scoped to
exactly two subject kinds - a map or a fixed/dynamic array - lowered as two
hardcoded, special-cased codegen strategies (an indexed loop for an array, a
bucket-array walk for a map). Explicitly not built: ranging over a string
(rune iteration), a bare integer (Go 1.22's `for i := range n`), a
struct/pointer/any other type, or any general/user-definable iterator
mechanism that either of the first two could otherwise have been built on
top of.

**Why scoped this narrowly:** the motivating request was explicit that this
is meant as a dedicated, hardcoded-for-performance construct for exactly the
two container types this language already has real runtime representations
for - not a stepping stone toward a general iterator protocol. Ranging over a
string would need real Unicode/rune-decoding machinery this language has
nowhere else yet (every string operation so far is deliberately
ASCII/byte-oriented); ranging over a bare integer has nothing to do with
iterating an existing container. A general iterator protocol is a
substantially bigger, separate feature - inventing it just to give array/map
ranging a "proper" foundation would have been solving a problem nobody asked
for yet.

**Why the one-binding form's map-binds-key/array-binds-index asymmetry is
exactly Go's own rule, not a design choice made here:** it's real, easy-to-
get-backwards-by-symmetry Go spec behavior worth recording precisely. Go's
own spec: for a map, `for k := range m` binds the key - there is no way to
get "just the value" from a one-binding map range. For an array/slice, `for
i := range a` binds the index, never the element - the natural (and wrong)
assumption by symmetry would be "the single binding is always the value" for
both, which is correct for neither. Implemented directly in
`sema.checkRangeForStmt` (the single name's `Type` is seeded from
`subjType.Key` for a map, `i32Type` for an array - never from `subjType.Elem`),
and proved with concrete runtime values, not just "it compiles," in
`src/codegen/rangefor_test.go` (`TestRangeForMapOneBindingBindsKey`/
`TestRangeForArrayOneBindingBindsIndex`, each using a single-entry container
where the key/index and stored value are deliberately different).

**Status:** shipped. See `LANGUAGE.md`'s new "Range loops" section and
`CODEGEN.md`'s new "Range loops" section for the two lowering strategies.
New coverage across `src/parser`/`src/sema`/`src/codegen` (all three binding
shapes, break/continue legality, the non-copyable-value-binding rejection
below, destructor unwinding on both normal completion and an early `break`),
plus a real worked example (`examples/range/range.llx`) exercised end to
end, JIT and AOT alike.

**A genuine gap found during independent verification, fixed before merge:**
the first implementation seeded key/value binding types (`checkRangeForStmt`)
without ever calling `checkNoIllegalCopy` - every iteration's binding is a
real copy out of the map/array's own storage, exactly like `v := m[k]`/`v :=
arr[i]` already are, but nothing enforced the identical non-copyable rule for
a range binding specifically. Ranging over a fixed array of a
destructor-owning element type silently compiled and ran, producing one
illegal extra destructor call per iteration on a value the language's own
copy rule says can never be duplicated - the exact "one layer's check is
looser than what a downstream layer can actually handle" failure mode
`AGENTS.md`'s review-process section exists to catch, caught here by testing
the claim in the first draft's own test comment against a plain `v := m[k]`
of the same element type, which is correctly rejected. Fixed by adding
`checkNoIllegalCopy` (`allowFresh=false`) to every real key/value binding.
The two `src/codegen` tests that had encoded the buggy premise as their own
fixture were rewritten to exercise the same destructor-unwind mechanism
legally instead, plus a new `src/sema` test
(`TestRangeForNonCopyableValueBindingRejected`) proving the rejection
directly.

The `=`-reuse form (`for k, v = range m {}`, rebinding already-declared
variables instead of declaring fresh ones) was left unbuilt - it doesn't fall
out of this round's own grammar detection for free, and was scoped as a
nice-to-have rather than a hard requirement.

---

## 2026-07-24 - Generator functions: push/callback lowering (not true coroutines), `yield T` as a return-type marker

**Decision:** the third piece of the "iterators" arc (after Rust-style
`match`/`yield` and hardcoded map/array `range`) - a `yield T` return-type
marker on a top-level `func` declares a generator function, lowered to a
push/callback strategy (the consuming range-for's own body becomes a
synthesized callback the generator calls repeatedly), the same shape Go
1.23's own `range-over-func` uses - NOT true suspend/resume coroutines.

**Why push/callback over true coroutines:** already settled in the
conversation that scoped this round - a real coroutine (a generator function
that genuinely suspends mid-body and resumes later, holding its own
independent stack/register state) needs either a real stackful-coroutine
runtime or CPS-transforming the generator's own body into a state machine at
compile time - both substantially bigger undertakings than this round's
actual scope. A push/callback lowering needs none of that: the generator
function's own body simply keeps running on the SAME stack the whole time,
calling back into the consumer once per yielded value - the exact mechanical
trick `LANGUAGE.md`'s own "Explicitly out of scope" list names directly (no
external `.Next()`, no two live generators stepped independently, no
mid-function suspend).

**Why `yield T` as a return-type marker over the alternatives considered:**
two others were on the table - inferring "this is a generator" purely from a
`yield` statement appearing somewhere in the body (no grammar change at
all), or a dedicated new `iter` keyword replacing `func` entirely.
Body-inference was rejected because a function's own signature should be
legible from its declaration alone, without scanning the whole body first -
exactly the same "read the signature, not the implementation" principle
multi-return `(T1, T2)` return types and this feature's own sibling,
match-expression `yield`, both already lean on. A new `iter` keyword was
rejected as an unnecessary second way to spell "this is a function" when the
existing return-type position already has room for exactly this kind of
marker.

**Why the `loopCtx` generalization (`returnFromCallback` mode) is the one
genuinely new codegen mechanism, not a parallel break/continue
implementation:** every other piece of this feature reuses existing
machinery wholesale - the fat-pointer/indirect-call convention first-class
functions/lambdas already established, the identical closure-capture
analysis a `FuncLit` already gets (just keyed by a `RangeForStmt` node
instead), `genIndirectCall`'s own extraction logic factored into a shared
helper. But `break`/`continue` inside the synthesized callback have no real
loop to branch within (the generator itself loops, by calling the callback
repeatedly) - giving `loopCtx` a discriminated mode (branch to a basic block,
XOR return a bool from the callback's own frame) let `genBreakStmt`/
`genContinueStmt` stay the ONE shared implementation every loop kind funnels
through, rather than forking a second, parallel implementation just for this
construct.

**Status:** shipped. See `LANGUAGE.md`'s new "Generator functions" section
for the full language-level rule and `CODEGEN.md`'s new "Generator
functions" section for the producer/consumer lowering and the `loopCtx`
generalization. New coverage across `src/parser`/`src/sema`/`src/codegen`
(the `YieldReturnType` grammar shape, the method-receiver restriction,
`return`'s restricted legality inside a generator's own body, zero/one/two-
binding consuming rules, nested generator composition, break/continue proved
with concrete values, and capture-analysis reuse), plus a real worked
example (`examples/generators/generators.llx`) exercised end to end, JIT and
AOT alike.

---

## 2026-07-24 - True suspend/resume coroutines: `async`/`await` as new keywords, real LLVM coroutine intrinsics, top-level-only, void-only

**Decision:** `async func Name(params) { body }` + a bare `await` statement,
lowered directly onto LLVM's own first-class coroutine intrinsics
(`llvm.coro.id`/`coro.begin`/`coro.suspend`/`coro.resume`/`coro.destroy`/
`coro.done`) rather than the push/callback strategy generator functions use -
a genuinely different feature, not a generalization of `yield T`. A caller
holds the resulting handle (`sema.TypeCoroutine`, a real, storable,
non-copyable value) and drives it by hand via `resume(h)`/`done(h)`/`delete
h`. See `BLOCKERS.md`'s former "True suspend/resume coroutines" entry (now
resolved, removed per that file's own convention) for the investigation that
preceded this round.

**Why `async`/`await` as new, distinct keywords rather than extending `yield
T`:** the two features have genuinely incompatible lowerings, not two facets
of one mechanism. A generator's `yield T` producer never really suspends -
it keeps running on the consumer's own call stack the whole time, calling
back into a synthesized callback; a coroutine's `await` performs a REAL
suspend (`llvm.coro.suspend`), physically returning control to whichever
external call is driving it, with its own independently-allocated heap frame
surviving across that boundary. Retrofitting `yield T`'s existing
push/callback producer to also support a real suspend point would mean
rebuilding it on top of `llvm.coro.*` anyway, at which point there's no
shared mechanism left to justify reusing the same keyword.

**Why top-level-only, no closures, this round:** deliberately the smallest
useful core primitive, mirroring how `range` shipped and was independently
verified before generator functions built on top of it. A coroutine's own
frame - which locals are live across which suspend point - is entirely
`CoroSplit`'s own problem once the pre-split IR exists; the frontend needs
zero capture-analysis work as long as `async func` stays a plain top-level
declaration rather than a `FuncLit`. Extending this to closures/lambdas, and
to one coroutine awaiting another, are natural next rounds explicitly left
for later.

**Why void-only (no declared return type) this round:** reading a finished
coroutine's own final result correctly needs `llvm.coro.promise`-based frame
storage - a real, bounded LLVM mechanism, but a genuinely separate, easy-to-
get-subtly-wrong addition on top of an already-large round whose own
destructor-correctness matrix was already the highest-stakes verification
work in this project so far. Declaring a non-void return type on an `async
func` is a clean diagnostic rather than a silent gap.

**Why per-`await` dedicated cleanup blocks rather than a saved-suspend-index
dispatch:** `llvm.coro.suspend`'s own per-call `switch` already gives each
suspend point a distinct case-1 (destroy) target for free - the
compile-time-tracked `Generator.destructors` stack (the exact same mechanism
every struct/enum destructor already uses) already has everything needed to
write each one directly, snapshotted at that exact point.

**The real bug, and why it's the one thing worth flagging loudest:** the
first working implementation conflated a `coro.suspend` switch's DEFAULT arm
with its destroy (case 1) arm - reasonable-looking, but wrong:
[LLVM's own documented shape](https://llvm.org/docs/Coroutines.html#coro-destroy)
reserves the default arm for a bare "just suspended, do nothing" outcome,
with destroy as its OWN explicit case. Conflating the two makes the ramp's
own "haven't been resumed or destroyed yet" sentinel land in the destroy
arm, running a live coroutine's cleanup unconditionally on its very first
call - confirmed as a real, reproducible double-destruct via JIT execution,
not caught by IR verification or a build (the module verifies fine and
JIT-executes without crashing; it just runs the wrong destructors, silently).
This is exactly the failure mode `AGENTS.md`'s review-process section exists
to catch, and the reason this round's own verification leaned on
JIT-executing every one of the N+1 destroy-point x explicit-delete/
scope-exit combinations rather than trusting the IR shape alone.

**Status:** shipped. See `LANGUAGE.md`'s new "Coroutines" section for the
language-level rule and `CODEGEN.md`'s for the intrinsic-level lowering.
Test coverage spans `src/parser`/`src/sema`/`src/codegen`/`cmd/llvmc`'s own
`coroutine_test.go` files (an exhaustive per-suspend-point destructor matrix
is `src/codegen`'s own, JIT-executed against the real optimized pipeline
since coroutine intrinsics are only lowered there) plus a worked example
(`examples/coroutines/coroutines.llx`) run JIT and AOT alike.

## 2026-07-24 - `coroutine` type keyword, and `std/scheduler`'s pointer-behind-Entry design

**Why a real `coroutine` type keyword now:** Round 1 only ever produced a
`TypeCoroutine` value via `:=` inference from an async call - there was no
way to spell it in a struct field, array element, or parameter's declared
type, blocking anything that needs to hold a handle longer-term (a
scheduler's own pending-entry list, chief among them). `typeFromSymbol`
resolves it exactly like `int`/`f64`/etc. - no `Elem`/parameterization,
since this language has no generics and coroutine return values stay out of
scope (see `LANGUAGE.md`'s "Coroutines" section). All of `IsNonCopyable`,
the dynamic-array-of-non-copyable rejection, and the fresh-construction
exception already keyed off `Type.Kind` alone, so making the type spellable
needed no relaxation of any of them - confirmed by tests asserting the
identical rejections still fire for a `coroutine`-typed field/param, not
just a `:=`-inferred local.

One real gap the new spelling exposed: codegen's own `programHasAsyncFunc`
gate (see `CODEGEN.md`'s new "Coroutines" subsection) assumed a coroutine
handle could only ever exist in a program that also declares an `async
func` - no longer true once `coroutine` is a real type name on its own.
Fixed by widening the gate to also scan for a `TypeCoroutine` anywhere in
`sema.Info.Types`, not by relaxing any sema-level check. A second,
independent review pass found the identical stale assumption in
`checkNoOptAsyncRestriction` (`src/compiler`) - a `coroutine`-typed
declaration with no `async func` anywhere still crashed LLVM's own
instruction selection under `-no-opt` instead of getting a clean
diagnostic, confirmed directly before the fix. Widened the same way.

**Why `std/scheduler.Schedule` takes `*Entry`, not `h coroutine`, despite
the sketch it started from:** this language has no move semantics - once a
value is bound to an ordinary parameter, it's an *existing* value from
`checkNoIllegalCopy`'s point of view, not a fresh construction, so
`e.Handle = h` inside such a method is rejected exactly like `g := f` is for
any other non-copyable type. There's no way to write a generic
"take ownership of a handle and store it" method at all under this
language's current non-copyable rules - only fresh construction directly at
the assignment site ever satisfies the illegal-copy check. So the calling
package must build `Entry` itself (`e := new Entry{}; e.Handle =
SomeAsyncFunc(...)`), and `Schedule` only ever takes the already-built
`*Entry` - a pointer, always copyable regardless of what it points to. This
also settles why `Entry.Handle`/`Entry.NextWait` are exported fields
(the calling package must write them directly) while `resumeAt` stays
unexported (purely `Schedule`/`Tick`'s own bookkeeping).

**Why `[]*Entry`, not `[]Entry`:** a `coroutine` field makes `Entry`
non-copyable, and this language already rejects a dynamic array of a
non-copyable element type outright (see `LANGUAGE.md`'s "Destructors"
section) - `[]*Entry` sidesteps that entirely, since a pointer is always
copyable regardless of its pointee, matching the `FileHandle`
resource-behind-a-pointer idiom `LANGUAGE.md` already establishes.

**Status:** shipped. `coroutine` tested in `src/sema/coroutine_test.go`
(valid var/field/param usage, and the same non-copyable/dynamic-array
rejections against a real `coroutine`-typed slot rather than only an
inferred one). `std/scheduler.Tick`'s own resume/reschedule/removal matrix
is `src/codegen/scheduler_test.go`, JIT-executed against the optimized
pipeline (`compileAndJITOptimized`, coroutine intrinsics only lower there)
- covering zero/one/multiple pending entries, multi-`await` reschedule with
a changing `NextWait`, simultaneous due entries, and removal not disturbing
neighboring entries. Worked example: `examples/scheduler_demo/
scheduler_demo.llx`, run JIT and AOT alike.

---

## 2026-07-24 - AOT linker flags: repeatable `-l`/`-L` on `llvmc`

**Decision:** extend `compileToExecutable`'s gcc argv with repeatable
`-L <dir>` / `-l <lib>` flags (dirs before libs).

**Why:** libc and default Win32 import libs already resolve without flags,
but third-party C libraries (anything not on mingw's default link line) do
not. Keeping AOT as shell-out-to-gcc (see the 2026-07-23 AOT entry) means
the natural place for this is the existing link command, not a new linker
or language-level "link" declaration.

**Status:** shipped, then extended so the same flags also feed JIT (see the
"JIT third-party libraries" entry below). See `CODEGEN.md`'s `-o` section
and `TestBinary_AOT_LinkLib`.

---

## 2026-07-24 - Pin module DataLayout/triple from the host TargetMachine

**Decision:** after `buildTargetMachine` in `finishPipeline`, set the
module's data layout (`tm.CreateTargetData().String()`) and target triple
(`tm.Triple()`) before `RunPasses` / object emit.

**Why:** FFI struct-by-value ABI coercion (and correct object emission in
general) needs the module to describe the same layout the TargetMachine
will emit for. Leaving both empty made aggregate C ABI a guess.

**Status:** shipped alongside the FFI struct-by-value round. See
`CODEGEN.md`'s Optimization pipeline / TargetMachine notes.

---

## 2026-07-24 - `cstring`: a new predeclared type, not a `string` ABI change

**Decision:** add `cstring` as its own predeclared builtin type (like
`string`/`bool`) lowering to a single raw `ptr` - not a mode/flag on
`string` itself - reachable only via two explicit conversions,
`cstring(s)`/`string(cs)`. This is the FFI round's second piece, after the
`-l`/`-L` linker flags: `extern func` could already declare a pointer
parameter, but every real C API taking/returning `char*` had no legal way
to actually get one from this language's own `string`.

**Why a separate type instead of relaxing `string`'s own extern
restriction:** `string`'s `{ptr, i32}` representation and `cstring`'s bare
`ptr` are genuinely different ABI shapes, not two views of the same data -
letting `string` itself cross an extern signature would need codegen to
either silently drop the length (data loss for a non-NUL-terminated string)
or implicitly marshal on every call (a hidden allocation this project's own
"no auto-marshal of `string` at extern call sites" rule for this round
deliberately avoids - see `LANGUAGE.md`'s "The `cstring` type" section).
A dedicated type makes the marshaling boundary an explicit, visible
conversion instead.

**Why skip the arena-copy for a literal argument:** `cstring("hello")`
recognizes its argument as a `StringLit` node directly (not a general
"is this constant-foldable" check) and reuses `constStringValue`'s own
already-NUL-terminated backing global as-is - the same convention `print`'s
own format-string globals already use. Every other `string` value (a variable,
a concatenation result, ...) genuinely needs the arena-copy-plus-NUL path,
since nothing else guarantees NUL termination.

**The real bug this round found, worth flagging:** the reverse conversion,
`string(cs)`, needs a real `strlen` call, and the `args()` builtin already
declares its own local `strlen` extern (`buildArgsInitFn`, args.go).
Giving `cstring`'s own conversion a second, independent local
`llvm.AddFunction(g.mod, "strlen", ...)` compiled and JIT-executed fine in
isolation, but a program combining `args()`-with-cstring, or - the case
that actually surfaced it - a user's own `extern func strlen(...)` alongside
`string(cs)`, failed at JIT-lookup time with `Symbols not found: [
strlen.1 ]`: LLVM silently renames a second declaration of an
already-used name rather than erroring, and the JIT can never resolve a
symbol literally named `strlen.1` against real libc. Fixed by
`strlenExtern` (runtime.go): look up `g.mod.NamedFunction("strlen")` first,
reuse whatever's already there, and only declare fresh if genuinely
nothing exists yet - now the single shared path both callers go through.
This same category of risk (a user's own `extern func` re-declaring
`malloc`/`printf`/`memcpy`/`memcmp`/`memset`, this package's other
unconditionally-declared internal externs) pre-dates this round and is
**not** fixed here - out of scope for a `cstring`-only round, tracked as a
follow-up rather than silently left unmentioned.

**Status:** shipped. See `LANGUAGE.md`'s "The `cstring` type" section and
`CODEGEN.md`'s "`string` representation"/"External functions (FFI)"
sections. Test coverage spans `src/sema` (the type itself, both
conversions, and explicit rejection from `==`/`print`/`+`/`len`) and
`src/codegen` (JIT-executed round trips through real libc `strlen`/
`strcmp`, plus the shared-`strlen`-declaration regression case above).

---

## 2026-07-24 - FFI struct-by-value: allowlist a POD struct, with real ABI coercion

**Decision:** let a named struct type cross an extern func signature
(`isFFISafeType`/`isFFISafeStructField`, `src/sema/typecheck.go`) iff every
field is itself FFI-safe, recursively - a numeric/bool/cstring/pointer, a
nested FFI-safe struct, or a fixed-size array of one (a fixed array is
FFI-safe as a struct *field* only, never as a bare parameter/return - a real
C array parameter decays to a pointer, which this compiler doesn't do
implicitly). `string`/`[]T`/a function type stay rejected everywhere,
including as a struct field.

**Why codegen needed more than "just declare it with `g.llvmType`":**
verified empirically, not assumed correct from the module's already-pinned
`DataLayout`/triple (see the `-l`/`-L` entry above) - a first attempt doing
exactly that produced a real, silent corruption: an AOT-linked call to a
real gcc-compiled `int point_sum(struct point p)` (`struct point { int x,
y; }`) returned `19` (`p.x` alone) instead of `42` (`p.x + p.y`). LLVM's
default aggregate-argument lowering flattens a direct (uncoerced) struct
parameter into one independent register/stack slot *per field*, but the
real Windows x64 ABI requires the opposite for a struct this size: the
*whole* 8-byte struct coerced into one integer register, never split.
(Confirmed against Microsoft's own x64 calling-convention documentation:
a struct/union of size 1, 2, 4, or 8 bytes is passed/returned as an integer
of that size; any other size is passed by reference, i.e. the caller
allocates a copy and passes its address, shifted in as a hidden first
argument for a return.) `src/codegen/ffi.go` implements this classification
directly (`abiSizeAlign`, computed from field types rather than LLVM's own
`TargetData`, which doesn't exist yet at codegen time) and `genFuncCall`
adapts each natural struct value to/from the coerced shape via a temp
alloca (`coerceExternArg`/`bitcastThroughMemory`) at every call site.

**Scoped to direct calls only.** This coercion lives in `genFuncCall`, not
`genFuncThunk`/`genIndirectCallValue` - a bare reference to a struct-by-value
extern func, later called *indirectly* through a first-class function value,
isn't covered. Not a functional regression (nothing exercised this before),
but a real, deliberate gap: LANGUAGE.md's only documented extern-func
calling form is a direct call, and generalizing the ABI coercion through the
`{fnPtr, ctxPtr}` closure convention as well was judged separate, non-trivial
scope not worth taking on speculatively this round.

**Status:** shipped. See `LANGUAGE.md`'s "External functions (FFI)" section
for the allowlist rule and `CODEGEN.md`'s section of the same name for the
lowering. Test coverage spans `src/sema` (POD/nested/array-field acceptance,
and rejection of a non-FFI-safe field, a bare fixed array, string/`[]T`/
func-typed fields) and `src/codegen` (declared-signature IR-shape assertions
for both the coerced-integer and indirect/`sret` cases), with the real
end-to-end proof in `cmd/llvmc`'s AOT suite: `TestBinary_AOT_LinkLibStructByValue`
(the 8-byte, coerced-integer case) and `TestBinary_AOT_LinkLibLargeStructByValue`
(the 12-byte, indirect/`sret` case), both linking a real gcc-compiled static
library via the existing `-L`/`-l` path.

---

## 2026-07-24 - `cfunc`: a new bare-C-function-pointer type, not a `TypeFunc` flag

**Decision:** add `cfunc(T1, T2) R` as its own keyword, AST node
(`CFuncType`), and sema `TypeKind` (`TypeCFunc`) - a bare C function
pointer (`sema.TypeCFunc` lowers to a plain `g.ptrTy`), distinct from an
ordinary `func` value's `{fnPtr, ctxPtr}` fat pointer (`sema.TypeFunc`/
`g.funcValTy`). Only a direct reference to a top-level `FuncDecl`/
`ExternFuncDecl` with a structurally matching signature may become one
(`checkFuncToCFuncConversion`, `sema/typecheck.go`) - a function literal or
any function value already sitting in a variable/parameter/field is a
compile error, not a silent fallback.

**Why a new `TypeKind` rather than a `TypeFunc{IsBare: true}` flag:** every
existing `TypeFunc`-aware switch (`Equal`, `String`, `llvmType`,
`isFFISafeType`, `funcSigForCall`, `genCallExpr`'s call-shape dispatch)
would otherwise need an `if t.IsBare` branch buried inside its `TypeFunc`
case rather than a parallel, equally-visible `TypeCFunc` case next to it -
the same reasoning `TypeCString` already established over reusing
`TypeString` with a flag (see that entry above). The two kinds share
`Params`/`Return` (identical shape, just a different calling convention),
so this costs no duplicated field, only a second explicit case per switch.

**Why the conversion is a sema-layer decision, not a codegen coercion:**
`cfunc` has no capture context at all - there is no way to synthesize a
context-free real C function pointer for an arbitrary closure without a
trampoline (a small per-closure stub allocated at run time, mapping a
fixed C ABI call back to a specific `{fnPtr, ctxPtr}` pair), which is a
real, separate feature of its own (dynamic code generation or a limited
fixed-arity dispatch table), not attempted this round. Restricting the
source to a direct top-level declaration reference sidesteps needing one
at all: that function's own address is already fixed and already has no
implicit context, so `checkFuncToCFuncConversion` just retypes the
reference in place (`info.Types[at] = want`) and `genExpr`'s `Ident` case
reads `g.funcs[sym].fn` directly - zero runtime cost, and genuinely
correct, rather than a coercion papering over a shape mismatch.

**The one real ABI hazard this surfaces: an ordinary (non-extern) `FuncDecl`
with a struct-by-value parameter/return can't convert.** An extern func's
own real signature is already built with `ffi.go`'s Windows x64 struct
coercion, matching a `cfunc` call site's identical coercion automatically.
An ordinary `FuncDecl`'s real signature instead uses this compiler's own
uncoerced, internally-consistent struct-passing convention (correct between
two intra-language call sites, but not what a `cfunc`-shaped, ABI-coerced
call site expects) - `checkFuncToCFuncConversion` rejects this shape
outright rather than let codegen produce a real, silent ABI mismatch.
Teaching an ordinary `FuncDecl` to carry a second, ABI-coerced calling
convention just for this case was judged separate, non-trivial scope.

**Status:** shipped. See `LANGUAGE.md`'s "External functions (FFI)"
section's own `cfunc` subsection and `CODEGEN.md`'s section of the same
name for the lowering. Test coverage spans `src/parser` (type-grammar
`Tree.Dump` shape), `src/sema` (extern-signature acceptance, FFI-safety
recursion, the conversion's happy path and every rejection - a closure, a
stored func value, a mismatched signature, and the struct-by-value/
non-extern-source case), `src/codegen` (bare-`ptr` IR shape, no
`extractvalue`/no `.thunk`, real JIT execution), and `cmd/llvmc`'s own
`TestBinary_AOT_LinkLibCFuncCallback` - a real gcc-compiled static library
that itself calls the passed function pointer, linked via the existing
`-L`/`-l` path.

---

## 2026-07-24 - JIT third-party libraries: unified `-l`/`-L` via ORC path generators

**Decision:** the same `-l`/`-L` flags used for AOT also feed the default
JIT path. Each `-l` is resolved under the `-L` dirs to a real `.dll` or
static `.a`/`.lib`, then attached with
`NewDynamicLibrarySearchGeneratorForPath` or
`NewStaticLibrarySearchGeneratorForPath` (after the existing process-wide
generator). `-emit-llvm` still rejects `-l`/`-L` (IR dump never loads
libraries). `-L` without `-l` is a usage error.

**Why not LoadLibrary + ForProcess only:** that would work for DLLs but
not for the static `.a` archives mingw game libs often ship, and it would
pollute the llvmc process rather than using ORC's intended path-based
generators (already wrapped in `third_party/go-llvm/orcjit.go`).

**Why skip `*.dll.a`:** mingw import libs don't contain the real object
code the static generator needs; accepting them would look like success
and then fail at symbol lookup. Error asks for the real `.dll` or a true
static archive.

**Status:** shipped. See `CODEGEN.md`'s "JIT third-party libraries"
section, `cmd/llvmc/linkresolve.go`, and
`TestBinary_JIT_LinkLibStatic` / `TestBinary_JIT_LinkLibDLL` /
`TestRun_JIT_MissingLibrary`.

---

## 2026-07-24 - `-watch`: hot-reload via persistent LLJIT + ResourceTracker

**Decision:** add an optional `llvmc -watch` driver mode (not a language
change). One LLJIT stays up; user code reloads by swapping an ORC
`ResourceTracker`. Convention: optional void `Init`, looping `Frame() int`
(`0` = continue). Flags `-init`/`-tick` override names. `main` unused under
`-watch`. Reset-on-reload (Init again; no preserved game heap). Last-good
module kept when a reload compile fails.

**Why not a host/game Module split yet:** v1 only needs edit-reload for a
single user package with third-party libs already on the JIT via `-l`.
Splitting a sticky host Module from reloadable game code can come later if
arena-preserve or live host state matters.

**Why Tick returns int:** gives a clean stop for games (`WindowShouldClose`
→ non-zero) and for tests, without inventing a separate `-watch-max-ticks`
escape hatch.

**Status:** shipped. See `CODEGEN.md`'s "`-watch`: hot-reload JIT" section
and `cmd/llvmc/watch.go`.

---

## 2026-07-24 - `move x`: reject conditional moves outright, extend fresh-construction to resolve the return-statement escape-analysis gap

**Decision:** add a `move x` prefix expression (`x` a bare identifier only)
as a second exception to the non-copyable copy rule, alongside fresh
construction, now legal everywhere including a return statement (see
`LANGUAGE.md`'s "move" subsection - this is exactly the gap the prior
`coroutine`/`std/scheduler` entry above worked around with `*Entry`
pointer indirection). Enforcement is entirely sema-side, flow-sensitive but
deliberately simple: a moved-from symbol is tracked per function, and a
value moved on only *some* of two converging paths (one `if`/`else` branch,
one `match` arm, or a loop iteration after the first) is rejected outright
as ambiguous, never reconciled.

**Why reject rather than reconcile:** a real per-iteration/per-branch fixed
point (tracking a "maybe moved" state and resolving it against every
runtime path) is the textbook-correct approach, but it's real added
complexity this round doesn't need: rejecting the ambiguous case outright
means codegen never has to *decide* whether a given entry is ambiguously
moved - no "moved" sentinel added to any type's representation, no runtime
check at a destructor call site. This did NOT turn out to mean zero codegen
changes at all, though (an earlier draft of this entry claimed exactly
that, before independent verification caught the gap): `removeDestructorEntry`
removing an entry from *anywhere* in `Generator.destructors`, not only the
top, broke two scope-boundary mechanisms that had always assumed otherwise -
see `CODEGEN.md`'s own "move x" subsection for both (a plain integer
`base`/`destructorBase` snapshot silently invalidated by a below-it removal,
and `genIfStmt`/`genMatchStmt`'s own blind post-branch `restoreDestructors`
resurrecting an entry every reachable branch had legitimately moved away).
Both fixes are still purely "recompute the right bookkeeping against
whatever's actually on the stack" - no new per-type runtime state, no
generalizing the coroutine-handle nil-guard to arbitrary struct types -
just correcting an assumption two existing mechanisms made that no longer
held once removal-from-anywhere became possible.

**The loop case specifically:** rather than a real back-edge dataflow join
(the loop-head state depending on both loop entry and the body's own exit
state, requiring fixed-point iteration), moving a symbol declared *outside*
the current loop, from inside that loop's own body, is rejected
unconditionally regardless of break/continue placement - a value declared
inside the loop body has no such restriction, being fresh every iteration
by construction. This is provably sound (a real unsoundness exists whenever
such a loop can iterate more than once) but strictly more conservative than
necessary: a move immediately followed by an unconditional `break`, never
reading the moved-from symbol again inside the loop, is rejected here even
though it's actually safe. Accepted as this round's own scope boundary,
matching the same "simpler and stricter over a real fixed point" trade-off
as the conditional-move rule above.

**Resolving the return-statement gap:** the prior entry's own `*Entry`
workaround existed because a fresh-value return had no way to prove
soundness without knowing, at every call site, that the callee always
hands back sole ownership. `move` sidesteps that: `isFreshConstruction`
now also treats a call to any function whose own declared return type is
non-copyable as fresh at that call's own use site - sound because such a
function could only have type-checked if every one of its own returns
already satisfied this same fresh-or-move rule, transitively guaranteeing
sole ownership with no per-function annotation needed.

**Independent finding, fixed in the same round:** `genAssignStmt`'s plain
`=` case never destructed an existing non-copyable target's old value
before overwriting it - a real, pre-existing leak (`f := Res(1); f =
Res(2)` never ran `Res(1)`'s destructor) independent of `move`, but one
`move` would have doubled: `y = move x` overwriting an already-live `y`
needs the identical fix to be sound. Fixed by `genAssignInto` (see
`CODEGEN.md`), reusing `destructorFuncFor`/`genDestructorCall`.

**Status:** shipped. Parser (`src/parser/move_test.go`), sema
(`src/sema/move_test.go` - every fresh-or-move call site, use-after-move in
every form, the if/else/match ambiguity rule both directions, the loop
restriction, and the factory-function-return reasoning above), and codegen
(`src/codegen/move_test.go` - JIT-verified destructor counts for a moved
struct at every call site, the reassignment-leak fix for both a struct and
a coroutine handle, and - added after independent review caught the two
scope-boundary bugs above - a value moved in both branches of an if/every
arm of a match actually destructing once at runtime, not zero or twice, and
composing correctly with an enclosing loop's own break) all covered.
`std/scheduler` itself was NOT migrated
to the now-legal `Schedule(h coroutine, ...)` shape this round - the
`*Entry` design from the prior entry still stands, left as a follow-up.

## 2026-07-25 - `std:`/`lib:` import schemes: a distinguished prefix, resolved against a sibling `std/` directory next to the running executable

Motivating problem: import resolution (see the "Cross-package imports"
entry above) is purely relative to the importing file's own directory, with
no module-root/manifest concept at all - fine for a project's own local
packages, but it meant reaching this compiler's own standard library from a
project living anywhere outside this repo's checkout required a literal
`../../../path/to/llvm_lang/std/mathutil`-style walk, breaking outright for
a project on a different drive or with no `../`-reachable path back to the
compiler at all. Every mainstream language distinguishes "a path to a
sibling file" from "a name in a known root" (Go's `import "fmt"` vs a
relative import, Python's absolute vs dot-prefixed relative imports) -
confirmed with the user this project should too.

**Syntax: a `scheme:path` prefix, not a bare (unprefixed) path.** A bare
path (no `./`/`../` at all) was the first design considered - "no dot
prefix" resolving against a root - but rejected: it silently reinterprets
whatever a project's own local subdirectory named `std` already means
today, with no visible marker at the import site that anything unusual is
happening. A `scheme:` prefix (`std:mathutil`, not `std/mathutil`) is
unambiguous by construction instead - a colon before a path's first `/` is
illegal in a Windows path outright, and this project only targets
Windows/mingw64 (no second platform to worry about yet - see this same
file's own "No non-Windows platform consideration" entry), so a real
relative path on this project's one supported platform can never
legitimately contain one there, matching why
Python deprecated implicit relative imports (PEP 328) for the identical
reason. Needs zero lexer/parser changes: an import path is already a plain
string literal, so the whole feature lives in `src/loader`'s own
string-to-directory resolution (`splitScheme`/`resolveImportPath`).

**`std` is the only scheme backed by anything; `lib` is reserved.** The
user explicitly asked for `lib:` to be recognized now (so a typo like
`ilb:x` gets a real "unknown import scheme" error, not a silent collision)
even though third-party package support - a registry, versioning, a
manifest file - doesn't exist and isn't being designed yet; that's a
substantially bigger, separate feature deliberately left for well after
this round.

**Where `std/` actually lives: on disk, next to the running executable -
not embedded in the binary.** Embedding the whole standard library into
`llvmc`/`llvmc-lsp` via `go:embed` was the first implementation attempted
(a single self-contained binary, zero install-location assumptions at
all) - reverted on the user's own direction before landing: assume instead
that however an end user gets this compiler, they end up with the
executable(s) and the `std/` source sitting in the same folder on disk,
mirroring this repo's own layout (`std/` a plain sibling of `examples/` at
the repo root, exactly where `build.ps1` already puts `llvmc.exe`/
`llvmc-lsp.exe`). `loader.StdlibFS` locates it via
`os.Executable`/`filepath.EvalSymlinks` plus a `"std"` subdirectory check,
wrapped as an ordinary `afero.Fs` (`afero.NewBasePathFs`) - the one
justified exception to this project's own afero-only disk-I/O convention,
since there is no afero equivalent for "where is my own running binary" at
all (process introspection, not file content access); the actual
directory-exists check and every subsequent file read still go through
afero normally.

**`LoadProgram` itself stays scheme-free by design; `LoadProgramWithOptions`
is the one that resolves anything.** `loader.LoadProgram(fs, root)` keeps
its original signature and behavior unchanged, with both `std:`/`lib:`
simply unavailable ("no standard library location was configured for this
run") unless a caller goes through the new `LoadProgramWithOptions(fs,
root, Options{StdFS: ...})` instead - keeping the base entry point
deterministic and fully unit-testable with no hidden dependency on the
calling process's own binary location. Real production entry points
(`cmd/llvmc`, `cmd/llvmc-lsp`) resolve `loader.StdlibFS()` once and pass it
through; `cmd/llvmc`'s own resolver is a package-level var
(`loaderOptionsFunc`), not a plain function, specifically so its own tests
can substitute a fake std root instead of depending on `os.Executable()`
returning something meaningful for a `go test`-compiled binary (which,
correctly, it never does).

**A real, concrete simplification this enabled:** `cmd/llvmc -test`'s
synthesized test driver used to locate `std/test` via `stdTestImportPath`,
walking upward from the entry package's own directory looking for a
`std/test` sibling *anywhere* above it, then computing a relative path back
down to it - exactly the same fragile, checkout-relative mechanism this
whole entry replaces, just duplicated for one specific caller. Deleted
entirely in favor of the driver simply writing a literal `import
"std:test"` - shorter, and correct regardless of where the entry package or
the compiler itself live, which the old walk-up mechanism was not.

**Status:** shipped (`src/loader` scheme resolution + `StdlibFS`,
`cmd/llvmc`/`cmd/llvmc-lsp` wiring, every example and `std/scheduler`'s own
doc comment migrated off the old relative-path form, `LANGUAGE.md`/
`docs/packages-and-stdlib.md`/`docs/compiler.md`/`docs/examples.md`
updated). Verified hands-on, not just via the test suite: built the real
`llvmc.exe`, copied it plus `std/` into a directory wholly outside this
repo's checkout, and ran a project living in a *third*, unrelated directory
importing `std:mathutil` from there - resolved and ran correctly. Not done
this round, left as a known follow-up: `src/lsp`'s own auto-import
completion (`completion_import.go`) still only ever suggests a relative
path for a project's own local packages (via `Workspace.PackageIndex`,
which only scans the current project's own tree) - it does not yet offer
`std:` packages as completion candidates at all.

## 2026-07-25 - Reserved words are legal as a struct field/method name, but not a var/free-function/type name

Motivating problem: an external agent working on the JetBrains plugin flagged that LANGUAGE.md's own "Structs" section example (`func (Point) move(dx int, dy int) { ... }`) didn't actually compile - `move` is a reserved keyword (`move x`, see "Destructors"), and every name-binding position uniformly rejected any keyword-tagged token via `expectIdent()`, method names included. Verified hands-on against the real compiler before doing anything else: confirmed the exact example failed to parse with "expected identifier, found move" at both the method declaration and its own call site.

**Resolved in favor of the parser, not the docs**, after weighing both: a field or method is only ever reached through `receiver.name` (member access) or declared inside a struct's own member list - never as a bare value on its own, unlike a `var`, a free function, or a type name, which can always appear standing alone where an expression is expected. `move` specifically dispatches unconditionally to its own `MoveExpr` prefix rule wherever a bare identifier-shaped token could appear as a value (`parseIdentExpr`'s own `Keyword` switch) - so a `var`/free-function *named* `move` would make the value `move` itself permanently uncallable, a real ambiguity; a method or field named `move` creates no such collision, since nothing else can ever appear at those two positions regardless of what identifier text is read there.

**New `expectMemberName`** (`src/parser/parser.go`) is `expectIdent`'s counterpart for exactly the two positions that need this: `parseField` (a struct field name) and `parseMemberExpr` (a member-access name after `.`) always use it; `parseFuncDecl`'s own name position uses it only when a receiver clause is present (a method), falling back to `expectIdent` for a free function. Constructor/destructor's own struct-member-start dispatch already routes those two keywords to their own grammar before `parseField` is ever reached, so no additional exclusion was needed for the struct-field case.

**One real, accepted limitation, not chased further this round:** a keyword-named field can be constructed positionally (`Point{1}`) but not via a keyed composite literal (`Point{move: 1}`) - a keyed element's own key is parsed as an ordinary expression first (`parseCompositeLitElem`), and `move` (like every other value-position keyword) always means the start of its own construct there, never a plain reference to a field of that name - fixing this would need a genuine lookahead (peek past the identifier for a following colon before ever attempting expression parsing), which the parser doesn't have today and wasn't judged worth adding for this one construction path when the positional form already works. Recorded in `docs/current-limitations.md`, not silently left undocumented - though its own real severity is worse than "one clean diagnostic": `move`'s own operand-parsing failure cascades into several stacked errors that also corrupt parsing of whatever follows in the same file, not a single isolated one, an existing property of every other value-position keyword used the same wrong way (`Point{true: 1}` behaves identically), not something this round introduced.

**Status:** shipped (`src/parser/parser.go`'s `expectMemberName`, wired into `parseField`/`parseMemberExpr`/`parseFuncDecl`). `src/parser/malformed_test.go`'s own pre-existing `TestKeywordRejectedAsMemberField` (asserting `a.if` was an error) was updated to `TestKeywordAllowedAsMemberField`, asserting the new, correct behavior instead of quietly deleting the coverage - the same "update, don't just delete, when a contract deliberately changes" discipline used earlier this session for `src/lsp`'s own parser-bailout-contract tests. New coverage added for a keyword-named method and struct field (`src/parser/decl_test.go`), and the boundary itself: `func move() {}` (a free function) is still correctly rejected (`TestKeywordRejectedAsFreeFuncName`). Verified end-to-end against the real compiler, not just the test suite: LANGUAGE.md's own exact example now builds and runs, computing the correct result; the keyed-literal limitation was independently confirmed to fail cleanly (a real diagnostic, not a crash or silently wrong value) rather than just asserted.

## 2026-07-25 - `std/collections`'s `SlotMap[T]` gains `Len`/`Clear`/`Values`/`Handles`; fixed a real bug in cross-package generator range calls

Motivating ask: `SlotMap[T]` had no way to iterate its own live entries, or ask how many it holds, or reset it - real gaps for a general-purpose container. Since a generator function can't be a method (see LANGUAGE.md's "Generator functions" section), `Values[T](s *SlotMap[T]) yield T` and `Handles[T](s *SlotMap[T]) yield Handle` are free, generic top-level functions instead - two separate generators, not one yielding a (Handle, T) pair, since this language's `yield T` syntax only ever names one type (no multi-value yield). `Len`/`Clear` are ordinary methods. A shared `liveSlots[T]` helper builds a same-length-as-items `[]bool` marking freed slots once per call (O(len(items) + len(freeList))), rather than either function re-scanning the free list per slot.

**Real bug found and fixed along the way, not just an addition:** the very first attempt to actually use `collections.Values(&m)`/`collections.Handles(&m)` in a range-for from a different package failed outright - "range over a generator requires calling it directly by name... not through a stored function value or any other indirection" - even though the call is exactly as direct as a same-package one. Root cause: `directFuncCallSymbol` (`src/sema/resolve.go`, shared by both the Resolve-phase shape check and Check-phase enforcement) only ever recognized an `Ident` callee, never a `MemberExpr` one (a package-qualified call, `pkg.Gen(...)`) - a gap nobody had hit before, since no stdlib package combined generators with real cross-package imports until this round. Confirmed codegen itself needed no change at all: `genRangeForGenerator` already reads `info.Refs[calleeNode]` generically, working identically whether that node is an Ident or a MemberExpr - the callee-shape check was the only place this was actually blocked. Fixed by widening `directFuncCallSymbol` to accept either node kind - plus an explicit exclusion for a MemberExpr callee resolving to a receiver-bearing FuncDecl (a real method call), caught by the required separate review pass: the first version of this fix assumed a method call could never reach here at all, since "a generator function can't be a method" - true as a *diagnosed* rule, but not a structural one, since `checkFuncDecl` only reports the error without clearing the offending method's own declared return type back off `TypeGenerator` - hands-on confirmed a method wrongly declared with `yield T` and called via `obj.MethodName(...)` in a range-for was silently accepted by the first version of this fix (harmless only because `HasErrors` already gates codegen off by that point, not because the assumption held).

**Status:** shipped. New `src/frontend/frontend_test.go` regression test (`TestRunProgram_PackageQualifiedGeneratorRangeCall`, a real two-package fixture) verified to fail against the pre-fix code and pass after. `std/collections/collections.llx`'s own new functions verified end-to-end against the real compiled `llvmc.exe`, not just type-checked: a new `examples/collections_test` package (11 `llvmc -test` assertions, all passing) covers the original API (Insert/Get/Remove/Valid) and every new addition together, including a struct-typed `SlotMap[Point]` instantiation and a double-`Remove`/slot-reuse generation-bump case.

## 2026-07-25 - `tests{}` blocks: same-file tests, kept out of normal builds

Motivating problem: a stdlib package's own tests (`std/collections`'s `SlotMap`) were forced into a separate package (`examples/collections_test`), making the test suite itself part of that package's real exported API surface rather than staying internal.

**`tests` (plural), not `test`:** `test` collides with the existing `std:test` package's own local import binding name (`test.Runner`, used pervasively) - this project's keyword lexing is context-free (every occurrence of a reserved word is keyword-tagged regardless of position, see `move`/`match`), so making `test` a keyword would break every file referencing `test.Runner`. `tests` was confirmed unused as an identifier anywhere in the codebase/stdlib/examples.

**Mechanism is a parse-time splice, not a `testMode` flag threaded through later stages.** `ast.Tree.NewNode` decides a node's final shape once, at construction; every downstream pass (sema, codegen, loader's import scan) only ever walks `Tree.TopLevelDeclsOfKind`, never a raw child walk. So `parser.ParseFile`'s new `testMode bool` parameter decides, once, what a `tests{}` block's parsed children become: spliced directly into the file's own top-level decls (test mode), or wrapped in one new `TestBlockDecl` node nothing ever queries by kind (normal mode, invisible for free). `src/sema`/`src/codegen` needed zero changes - confirmed, not assumed, via dedicated test coverage in `src/loader` and `cmd/llvmc`. `loader.Options.TestMode` gates this to the entry package only (never a transitively-imported dependency's own `tests{}` blocks), matching `cmd/llvmc -test`'s pre-existing entry-package-only test discovery. `src/lsp` always loads with `TestMode: true` - a developer editing test code still wants IDE support for it, matching how `gopls` treats `_test.go` files as first-class.

**Status:** shipped (`src/enums`, `src/parser`, `src/loader`, `cmd/llvmc`, `src/lsp` wired; `LANGUAGE.md`/`docs/compiler.md`/`docs/feature-index.md`/`docs/examples.md`/`docs/current-limitations.md`/`docs/packages-and-stdlib.md` updated). `std/collections`'s own `SlotMap` tests migrated from `examples/collections_test` into a `tests{}` block at the end of `collections.llx`; the old example package deleted. Verified hands-on against the real compiled `llvmc.exe`: `llvmc -test std/collections` discovers and runs all 13 migrated assertions, and the same file's plain `-emit-llvm` output contains no mention of `TestXxx`/`std:test`/`Runner`.

## 2026-07-25 - Subject-less `match` considered, deferred: no clean way to let every true arm fire without breaking match-as-expression

Motivating ask: sugar for `match { cond => body, ... }` with no subject at all (Go-tagless-switch-style), for independent per-frame checks like movement input where holding both `A` and `W` should run both arms, not just the first.

**Decision:** not building this - deferred, not a hard rejection of the sugar itself. The sugar part (an implicit `true` subject, still first-match-wins, still requiring the mandatory `_` wildcard - see "Value matching" in `LANGUAGE.md`) is a real, low-risk convenience on its own. What's shelved is specifically "let every true arm fire independently."

**Why:** `match` already works as an expression (see "`match` as an expression"), which requires selecting exactly one arm's value - that's the construct's core invariant everywhere it appears. Letting a subject-less `match` run multiple true arms as a *statement* would make the identical `cond => { ... }` arm syntax mean two different things (exactly-one vs however-many) depending on context, with nothing at the call site to signal which - a worse footgun than just writing separate `if` statements, which is what this use case actually wants. No variant of subject-less match was found that keeps "exactly one arm, always" consistent while also solving the multiple-true-conditions case, so punting to `if` there is deliberate.

## 2026-07-25 - `-watch` JIT session failed on any function whose stack frame needs a probe: `___chkstk_ms` unresolvable

Motivating bug report: a real raylib game's `-watch` session that "was previously working fine" started failing with `JIT session error: Symbols not found: [ ___chkstk_ms ]` once `Init`'s own local variables grew large enough. Reproduced independently with a minimal repro (no raylib needed): a `[4000]i64` local array, forced to survive optimization by escaping through a real extern call (`QueryPerformanceCounter`, already an existing FFI example in `LANGUAGE.md`) rather than being folded away as dead code.

**Root cause:** LLVM's `x86_64-w64-windows-gnu` backend auto-inserts a call to `___chkstk_ms` in a function's prologue once its stack frame crosses roughly one page (the standard Windows stack-probe convention, so the OS's guard pages get touched incrementally instead of possibly being skipped over). `___chkstk_ms` is a real symbol - statically linked into `llvmc.exe` itself from libgcc - but it's an internal helper, never a DLL export, so `NewDynamicLibrarySearchGeneratorForProcess`'s generic per-process symbol search (the mechanism most JIT'd calls resolve through) can never find it. This is the exact same class of gap `bindMinGWMainThunk` already works around for `__main`/`__argc`/`__argv` (see that entry's own dated log entry) - just not yet extended to this symbol, since nothing had needed a large enough stack frame to trigger it before.

**Fix:** `cmd/llvmc/chkstk.go` (new, tiny cgo shim, same shape as `watch_stdio.go`'s existing `setStdoutUnbuffered`) takes `___chkstk_ms`'s own real address via a one-line C helper, and `bindMinGWMainThunk` binds it as an absolute symbol alongside `__main`/`__argc`/`__argv` - see that function's own doc comment (main.go) for why this one, unlike `__main`, has to resolve to the genuine implementation rather than any harmless stand-in.

**Status:** shipped. New `cmd/llvmc/watch_test.go`'s `TestBinary_Watch_LargeStackFrameInit` reproduces the bug end-to-end as a real subprocess test (confirmed to fail before the fix, pass after) - not just a `go test` unit check, since this is JIT/backend behavior that only manifests through the real `-watch` path. Verified by hand against the actual built `llvmc.exe` too, including confirming the failure was reliably reproducible before the fix and reliably gone after (re-run multiple times, not a one-off).

# Codegen (`src/codegen`) and the `llvmc` CLI driver

How the compiler lowers the language described in `LANGUAGE.md` into LLVM
IR, plus the `cmd/llvmc` CLI that actually runs a compiled program. See
`AGENTS.md` for the doc split this file is part of, and `DECISIONS.md` for
the "why" behind choices summarized here in passing (numeric widths, the
arena allocator's design, etc).

Lowers a resolved+type-checked `*ast.Tree`/`*sema.Info` pair into an LLVM
`Module` via `tinygo.org/x/go-llvm`. Assumes its input is already fully
valid (zero diagnostics from `sema.Resolve`/`sema.Check`) - it is lowering
already-correct code, not re-deriving semantics, so most invalid input is a
panic, not a diagnostic. The exceptions - real codegen-level restrictions
sema has no opinion on - are documented below and still produce a proper
`diag.Bag` entry.

## `int` is 32-bit

`int` lowers to `i32`, not `i64`. This isn't just "pick one, they're
symmetric": `main`'s real LLVM signature must return `i32` (the actual OS
process exit code), so with `int` == `i32` a source-level `func main() int {
return code }` needs no truncation/cast at all - the language's own `int`
and the platform C ABI's `int` are simply the same type. See `DECISIONS.md`.

## Numeric type -> LLVM type, and where the type resolution itself lives

`sema.Type`'s six concrete numeric kinds map straight onto go-llvm's own
integer/float constructors (`llvmType`, `src/codegen/types.go`): `i8`/`i16`/
`i32`/`i64` -> `Int8Type`/`Int16Type`/`Int32Type`/`Int64Type`; `f32`/`f64` ->
`FloatType`/`DoubleType`. An LLVM integer/float instruction (`CreateAdd`,
`CreateICmp`, ...) is already generic over bit width as long as both
operands share the same LLVM type - which sema guarantees by construction
(see `LANGUAGE.md`'s Types section) - so no per-width branching is needed
inside `genBinaryExpr`/`genUnaryExpr`/etc.; only the *kind* (integer vs.
float) needs to be checked, to pick the matching real floating-point
instruction (`CreateFAdd`/`CreateFSub`/`CreateFMul`/`CreateFDiv`/
`CreateFCmp`/`CreateFNeg`) instead of the integer one.

There is **no codegen-side type-position resolution left at all** - a prior
version of this package had its own `resolveTypeNode`/`varDeclType`
functions that re-derived a type-position node's `sema.Type` from scratch
(duplicating `sema/typecheck.go`'s own `computeTypeFromNode`/`declType`
logic, entirely because `sema.Info.Types` didn't yet cover type-position
nodes - only value-expression ones). That gap is closed: `sema.Check` now
stores every type-position node's resolved `Type` into `Info.Types` too - a
`VarDecl`/`Param`'s own declaration node (`declType`), a `VarDecl`/`Param`/
`Field`/`FuncDecl` return type's annotation node and an `ArrayType` node and
its element (`typeFromNode`) - so every codegen call site that used to call
`resolveTypeNode(n)` or `varDeclType(decl)` is now just a plain
`g.info.Types[n]` map lookup, same as any other expression node. This was a
deliberate architectural fix, not a refactor for its own sake: two
independent implementations of "what type does this node have" is exactly
the kind of duplication that silently drifts out of sync over time (this
round almost re-created it for the six new numeric types, before switching
directly to the `Info.Types` fix instead). See `AGENTS.md`'s `## Architecture`
section - this is the exact mistake that principle now exists to prevent.

## Explicit conversions, concretely

`T(x)` (see `LANGUAGE.md`'s Types/Explicit-conversions sections) is
recognized in codegen (`isConversionCall`, `src/codegen/expr.go`) exactly the
way sema determined it - a `CallExpr`'s callee `Ident` resolving (via
`Info.Refs`) to `sema.SymBuiltinType`, not a function - and lowered
(`genConversion`) using the correct LLVM instruction for the source/target
type pair: `CreateSExt` (widening int - every integer here is signed, so
sign-extension is always correct, never zero-extension), `CreateTrunc`
(narrowing int), `CreateSIToFP`/`CreateFPToSI` (int<->float), `CreateFPExt`/
`CreateFPTrunc` (widening/narrowing float). A same-`Kind` "conversion" (e.g.
`i32(someI32Value)`) is recognized up front and just returns the value
unchanged - no pointless bitcast/identity instruction emitted for it.

## `print`'s printf format specifiers - a real Windows/mingw64 platform gotcha

Every numeric width needs its own `printf` format specifier (`genPrintCall`/
`genPrintValueBare`, `src/codegen/runtime.go`): `i8`/`i16` are sign-extended
to `i32` first (`CreateSExt`) and reuse `"%d"` - a manually-built variadic
`printf` call like this one doesn't get C's own default-argument-promotion
for free, so this package has to do it explicitly, the same reasoning that
already applied to `bool`. `f32` is extended to `f64` first (`CreateFPExt`)
and reuses `"%f"` - variadic C calls always promote `float` to `double`,
same rule. `f64` uses `"%f"` directly.

`i64` needed to be **verified empirically, not assumed** - this project's
toolchain is mingw64/MSYS2 (see SETUP.md), and `%lld`/`%d`-family format
specifier behavior for 64-bit integers can genuinely differ between an
MSVCRT-style `printf` (whose *historic* implementation doesn't understand
`%lld` at all - it wants the MS-specific `%I64d`) and mingw-w64's own
"ANSI stdio" `printf` wrapper (`__USE_MINGW_ANSI_STDIO`, on by default for
x86_64 mingw-w64 builds), which *does* support `%lld` correctly, C99-style.
This was tested directly, not guessed at: `src/codegen/stdoutcapture`
redirects the real C-runtime `stdout` FILE* (via `freopen`, from a small
non-test cgo file - `go test` flatly rejects `import "C"` inside any
`_test.go` file, "use of cgo in test ... not supported", a real toolchain
restriction hit while building this, hence the separate package) so a test
can capture exactly what a JIT-executed `print` call writes to real stdout,
byte for byte - something no prior round of this project had a way to do.
`TestPrintI64FormatSpecifierIsCorrect`/
`TestPrintNegativeI64FormatSpecifierIsCorrect` (`src/codegen/stdout_capture_test.go`)
print a value that doesn't fit in 32 bits (`4000000000`) and a negative i64
value, and assert the captured bytes are exactly correct. **Result: `%lld`
is correct on this toolchain** - mingw64's ANSI-stdio printf handles it
properly, confirmed via a real, byte-exact captured comparison, not
assumption. If this project's toolchain ever moves off mingw64/MSYS2 (a
plain MSVC-linked build, for instance), this is the first thing to
re-verify - the wrong specifier wouldn't crash, it would silently print
garbled digits.

## `string` representation

`string` is the literal (unnamed) LLVM struct `{ ptr, i32 }` - a data
pointer plus a length, **not** a null-terminated C string. Every consumer
(`print`, `+`/`+=` concatenation, `==`/`!=`, `< <= > >=`) goes through the
length field, never `strlen`. Concatenation (`genStringConcat`,
`src/codegen/runtime.go`) asks the arena allocator (see below) for a buffer
sized to fit both operands and `memcpy`s each in. Equality (`genStringEqual`)
short-circuits on length mismatch before ever calling `memcmp`. Ordering
(`genStringOrder`/`genStringCompareSign`) is a real byte-by-byte
lexicographic comparison, same as Go: `memcmp` over the shorter operand's
length decides it whenever the two differ within that shared prefix, and the
lengths themselves break the tie when one string is a prefix of the other
(so `"ab" < "abc"`, matching Go).

## The arena allocator (`src/codegen/runtime.go`'s `setupArena`)

Every codegen-level heap allocation goes through one centralized bump
allocator instead of calling libc `malloc` directly at each call site -
currently that's just string concatenation (`genStringConcat`), but any
future heap-needing feature (e.g. dynamic arrays, if/when they're designed)
should route through this same primitive rather than reintroducing scattered
`malloc` calls. See `DECISIONS.md` for why this shape was chosen over the
alternatives.

Design: **one process-lifetime arena, growing in malloc'd chunks** (64KiB
each, `arenaChunkSize`) - not a single fixed-size block reserved up front, and
not a `malloc`-per-request scheme either. It's a real generated LLVM function
(`llvm_lang.arena_alloc`, an internal-linkage function this package builds
directly at `Generate` time - not a libc call), backed by two mutable
globals: `.arena.cursor` (the next free byte in the current block) and
`.arena.remaining` (how many bytes are left in it). Allocating `size` bytes:
if the current block doesn't have `size` bytes left, `malloc` a fresh block
first (`arenaChunkSize`, or exactly `size` for a single request bigger than
that) and point the arena at it - whatever was left in the abandoned block is
simply never reused, not reclaimed; then bump the cursor forward by `size`
and hand back the pre-bump address.

This remains a real, intentional memory leak overall - **no `free`, no GC,
no refcounting** - exactly as before, just centralized behind one primitive
instead of ad hoc `malloc` calls at each use site. See `BLOCKERS.md`: a real
memory-management strategy (actual scoped frees, refcounting, a real GC) is
still an open, deliberately-deferred question for the user to decide - this
arena is groundwork for that future decision, not an attempt to answer it.

## Array bounds checking

Indexing a fixed-size array - both a read (`a[i]`) and a store
(`a[i] = v`) - lowers to a real runtime check, not a bare GEP: `i < 0 || i >=
size` (`size` is always known at compile time - see `LANGUAGE.md`'s Array
sizes section) traps immediately via LLVM's `llvm.trap` intrinsic (declared
as a plain extern `void()` function named exactly `llvm.trap` in
`setupRuntime`, `src/codegen/runtime.go` - LLVM recognizes the `llvm.`
prefix as an intrinsic regardless of how it's declared in the IR) followed
by `unreachable`, rather than ever proceeding to read/write through an
out-of-range address. See `genBoundsCheck`, `src/codegen/expr.go` - the same
`CreateCondBr`/basic-block shape `if`/`for` lowering already uses elsewhere
in this package.

```go
a := [5]int{1, 2, 3, 4, 5}
a[2]      // fine
a[5]      // traps - index == size
a[-1]     // traps - negative index
```

This is a hard process abort (the same failure mode any other LLVM `trap`
produces) - there's no exception handling or panic/recover mechanism in this
language, so an out-of-range index is unrecoverable by design, not merely
undetected. See `TestOutOfBoundsIndexTraps` (`src/codegen/bounds_test.go`)
for how this is tested despite that: JIT-executing a genuinely out-of-range
index would crash the `go test` process itself, so that test re-execs the
test binary as a child process and asserts the *child* exits abnormally -
the same `GO_WANT_HELPER_PROCESS` pattern `os/exec`'s own test suite uses -
rather than ever tripping the trap in-process.

## Global `var` initializers must be compile-time constants

A top-level `var`'s initializer must be foldable at compile time
(`codegen/constfold.go`'s `constExpr`: literals, parenthesization, unary
`-`/`!`, binary arithmetic/comparison/logical/string-concatenation, and
struct/array composite literals built entirely from constants). There is
**no** synthesized Go-style init-routine-that-runs-before-main - a
non-constant initializer (a call, a reference to another variable, `this`,
a member/index expression) is a codegen-level diagnostic, not silently
accepted and not a sema error (sema type-checks it fine; the restriction is
purely about what codegen is willing to lower a *global's* initializer to).
*(This section is expected to be rewritten once the implicit-global-init
round lands - see `DECISIONS.md`.)*

## The `print` builtin, concretely

`print(x)` lowers to a call into libc's `printf` (declared extern - there's
no runtime of this language's own yet): every numeric width gets its own
format specifier (`"%d\n"` for `i8`/`i16`/`i32` - the narrower two widths are
sign-extended to `i32` first, see "print's printf format specifiers" above -
`"%lld\n"` for `i64`, `"%f\n"` for `f32`/`f64` - `f32` is extended to `f64`
first, matching C's own variadic float-promotion rule), a string argument
uses `"%.*s\n"` (the explicit length means a non-null-terminated string
value never needs `strlen`), and a bool argument selects between two cached
`"true"`/`"false"` string values first. **print always appends a trailing
newline** - there's no separate "print without newline" builtin.

A struct or array value is rendered recursively, Go-`fmt`-`%v`-inspired (not
an exact match of Go's own output, just a reasonable pick - see
`genPrintStructValue`/`genPrintArrayValue`, `src/codegen/runtime.go`):

- a struct prints as `{f0 f1 ...}` - each field's value, space-separated, in
  declaration order, wrapped in braces.
- an array prints as `[e0 e1 ...]` - each element's value, space-separated,
  in index order, wrapped in brackets.
- a struct/array-typed field or element is rendered the same way,
  recursively - e.g. a struct of two `Point{x, y int}` fields prints as
  `{{1 2} {3 4}}`.

This is built from repeated `printf` calls (one per field/element, plus one
per punctuation character) rather than one combined format string - there's
no way to know a struct/array's shape at format-string-construction time in
general, so each piece is its own call. Every nested field/element uses a
"bare" (no trailing newline) format string; only the outermost `print(...)`
call appends the actual trailing newline, once, after the whole value has
finished rendering.

## First-class functions: fat pointers, and direct vs. indirect calls

See `LANGUAGE.md`'s "First-class functions" section for the language-level
feature (a free function's name is now a value) and `DECISIONS.md` for why
the representation below was chosen the way it was.

**Representation.** `sema.TypeFunc` maps to the literal (unnamed) LLVM
struct `{ ptr, ptr }` - a "fat pointer" of `{ fnPtr, ctxPtr }` (`llvmType`,
`src/codegen/types.go`). A bare, uncalled reference to a declared free
function (`add`, not `add(...)`) builds this struct directly
(`genFuncValue`, `src/codegen/expr.go`): `fnPtr` is the function's real LLVM
value, `ctxPtr` is always `llvm.ConstNull(g.ptrTy)`. That null is
deliberate, not a placeholder to fill in later by accident - every function
value this round is a free-function reference, so there's never a receiver
to close over yet. `genFuncValue` is the *one and only* construction site
for this struct, and is commented as the exact extension point a future
bound-method value (`p.move` referenced without a call) will use instead -
closing over the receiver's own address as `ctxPtr` rather than null - so
the representation and calling convention need no redesign when that
lands. Passing/returning/storing a function value moves this two-field
struct like any other small aggregate value, the same convention already
used for structs/arrays/strings (see "Structs/arrays/strings are passed and
returned as real LLVM aggregate types" below).

**Direct vs. indirect calls.** `genCallExpr`'s dispatch (`src/codegen/expr.go`)
mirrors sema's own (`funcSigForCall`, `src/sema/typecheck.go`) exactly, so
there is exactly one place on each side of the pipeline that decides which
of the two a given call is:

- A **direct** call - callee is a plain `Ident` resolving (via `Info.Refs`)
  to an actual declared free function (`sema.SymFunc` with a real `FuncDecl`,
  i.e. `Decl != InvalidNode` - `isDirectFuncCall`) - compiles to a plain,
  ordinary `call` instruction (`genFuncCall`), exactly as before this round:
  looks the callee's LLVM function straight up in `g.funcs` and calls it.
  The fat-pointer representation is never constructed or touched for this
  case at all - zero indirection overhead for the common case of calling a
  function by its own name.
- An **indirect** call - anything else that type-checked as callable: a
  function-typed variable/parameter, or any other expression whose value is
  itself a function (e.g. a call whose own result is a function, so
  `getAdder()(x)` chains straight through) - goes through `genIndirectCall`:
  evaluate the callee as an ordinary value expression to get its fat-pointer
  struct, `ExtractValue` out `fnPtr`, build the `llvm.FunctionType` to call
  through directly from the callee's own `sema.Type` (`Params`/`Return` -
  there's no `FuncDecl` node backing an indirect callee the way a direct
  call's `g.funcs` lookup has), and `CreateCall` through that raw pointer.
  `ctxPtr` is extracted from the struct but never passed along as a hidden
  argument - there's nothing yet that consumes it.

```llvm
; func apply(fn func(int) int, x int) int { return fn(x) }
define i32 @apply({ ptr, ptr } %0, i32 %1) {
  %3 = extractvalue { ptr, ptr } %2, 0
  %5 = call i32 %3(i32 %4)
  ...
}

; apply(double, 5) - a direct call passes a literal fat-pointer constant,
; ctxPtr always null:
%4 = call i32 @apply({ ptr, ptr } { ptr @double, ptr null }, i32 5)
```

## `main` is the real entry point

The function literally named `main` (no receiver) becomes the real LLVM
`i32 @main()` C entry point, regardless of whether the source declares a
return type for it: a bare `return`/falling off its end becomes `ret i32 0`
(a real, valid exit code, never `unreachable` - see the terminator-safety
section below), and `return expr` returns `expr` directly (typed `int` ==
`i32`, so no cast is ever needed - see above).

## Local variables: entry-block allocas

Every `var`/short-var-decl/parameter gets a stack slot via `alloca`, always
hoisted into the function's **entry** block (`createEntryAlloca`), never
emitted at the literal point of declaration. This matters for correctness,
not just style: an `alloca` inside a loop body's own basic block is a fresh
stack allocation on *every dynamic execution* of that block, not just a
lexical one - hoisting to the entry block means a `var`/`:=` declared inside
a loop allocates once and is simply re-stored each iteration.

## Method receivers: an implicit pointer parameter

A method's receiver (see `LANGUAGE.md`'s Structs section - always implicit,
always by-reference) lowers to a real, explicit first LLVM parameter of
type "pointer to the receiver's struct type". A method call's receiver
expression is lowered to its *address* (`genAddr`), never a loaded copy, so
a mutating method's writes are visible to the caller. `this` inside the
method body is that same pointer parameter directly - it needs no `alloca`
of its own, since it already *is* an address.

## Structs/arrays/strings are passed and returned as real LLVM aggregate
## types, not manual `sret`/by-ref tricks

A struct, fixed-size array, or string value used as a plain function
parameter or return type lowers to LLVM's own aggregate/array type directly
(e.g. a two-field struct becomes the LLVM struct type `{i32, i32}` used
as-is for a parameter or return type) - LLVM's own backend ABI lowering
handles whatever register/hidden-pointer convention the target actually
needs, transparently, the same way it does for any other LLVM-to-LLVM call.
This works because every caller and callee in a given module is generated
by this same package; there's no need to match an external C ABI by hand.

## Terminator safety (LLVM requires every basic block to end in one)

`sema.Check` now runs a full "does every path return" flow analysis (see
`LANGUAGE.md`'s "Missing return" section) and rejects any function declaring
a return type whose body isn't guaranteed to return. Codegen still keeps a
fallback for whenever a function's lowered body falls off the end without
already ending in a terminator, purely as a defensive backstop (a validated
tree should never actually need it - see `emitFallbackTerminator` in
`src/codegen/func.go` for the full reasoning):

- `main`, and any function declaring no return type at all, get a real,
  correct terminator (`ret i32 0` / `ret void` respectively) - falling off
  the end of a void function is legitimate (same as Go, and sema places no
  termination requirement on it either), and `main` must always hand a real
  exit code back to its OS caller, never undefined behavior.
- Any other non-void function gets `unreachable` instead - reaching this
  given sema's guarantee should be impossible; `unreachable` records that
  assumption directly in the IR rather than inventing a fake return value
  that could silently mask a real bug, in case the flow analysis itself
  ever has a gap.

Within a single block, a statement that itself terminates (`return`,
`break`, `continue`, or an `if`/`else` where both branches terminate) simply
stops that block's remaining statements from being generated at all - there
is no `goto`/labels in this language, so nothing could ever jump back into
that dead code anyway.

## Multi-file packages: one shared Module per package

See `LANGUAGE.md`'s "Multi-file packages" section for the language-level
model (directory = package, non-recursive, shared scope). `GeneratePackage`
(`src/codegen/codegen.go`) is the multi-file counterpart to `Generate`
(now a thin wrapper: `Generate(tree, info, name)` is exactly
`GeneratePackage([]*ast.Tree{tree}, map[*ast.Tree]*sema.Info{tree: info},
name)`): it takes every file's `*ast.Tree` plus its own already-resolved/
checked `*sema.Info` (see `sema.ResolvePackage`/`sema.CheckPackage`) and
lowers all of them into **one shared `llvm.Module`** - not one module per
file with cross-module linking, which this package has no need for: every
file in a package always ends up in the exact same module regardless, so
introducing separate per-file modules would only add real linking
complexity (symbol visibility, declaring externs for cross-module calls)
to solve a problem that doesn't exist here. See `DECISIONS.md` for this
choice recorded as a dated decision.

**Why no cross-file plumbing was needed inside a single function body.**
Every codegen-internal lookup that resolves a *use* of a declared symbol -
`Generator.funcs` (a call's callee), `Generator.globals` (a global var
reference), `Generator.structLayouts` (a struct's field GEP indices/LLVM
type) - is keyed by `*sema.Symbol`/`*sema.StructInfo` pointer identity, not
by `ast.NodeIndex`. A `NodeIndex` is only meaningful relative to the one
`*ast.Tree` it came from (see `ast.NodeIndex`'s own doc comment) - two
unrelated declarations in two different files can share the same NodeIndex
value - but a `*sema.Symbol`/`*sema.StructInfo` pointer is already globally
unique the moment it's allocated (one fresh value per declaration, shared
by every file's own `Info.Refs`/`Info.Structs` - see `sema.ResolvePackage`),
so keying on it sidesteps the cross-tree ambiguity entirely. `funcs` used to
be keyed by the FuncDecl's own `NodeIndex` before this round; that's the one
lookup this round had to change (`declareFuncSignature`/`genFuncBody` now
fetch the function's own `*sema.Symbol` via `Info.Refs` first) - every other
map was already keyed correctly and needed no change at all.

Because of that, `genPackage` (the multi-file counterpart to the old
`genFile`) never needs to switch "which file's nodes am I currently
reading" *in the middle of* lowering one declaration's body, unlike
`sema.CheckPackage`'s checker (see `sema/typecheck.go`'s own doc comment on
this exact point): every pass (`declareStructType`/`defineStructBody`,
`genGlobalVarDecl`, `declareFuncSignature`, `genFuncBody`) only ever reads
nodes that are children of the one declaration currently being processed -
always in the same file - and a cross-file *reference* inside a function
body (a call to a function declared elsewhere, a struct type named
elsewhere) is resolved purely through the pointer-keyed maps above, which
are already fully populated for the *whole package* by the time any
function body is generated (every file's structs are declared before any
file's globals; every file's globals before any file's function
signatures; every file's function signatures before any file's function
bodies - see `genPackage`). `Generator.tree`/`Generator.info` still switch
per file, but only at the top of each pass's per-file loop, never mid-body.

## Imports: still one shared Module, now for the whole program

See `LANGUAGE.md`'s "Imports" section for the language-level model
(directory = package, `import "path"` resolved relative to the importing
file, exported-only cross-package access). Nothing above changes at all:
`GeneratePackage` is simply called with *every package's* trees/infos in the
whole program flattened into one `[]*ast.Tree`/`map[*ast.Tree]*sema.Info`
(built by `cmd/llvmc`'s `compileAndRunProgram` from `loader.LoadProgram`'s
already-resolved import graph, via `sema.ResolveProgram`/`CheckProgram` -
see their own doc comments) - there is still only ever **one shared
`llvm.Module` for the entire program**, never one Module per package linked
together, for exactly the same reason multi-file support already gave for
one Module per *file*: every `*sema.Symbol`/`*sema.StructInfo` lookup
(`Generator.funcs`/`globals`/`structLayouts`) is already keyed by pointer
identity, not by which tree - let alone which package - originally declared
it, so a package boundary is no more special to codegen than a file
boundary already was. This was verified, not assumed: `genPackage`'s own
five passes (structs, struct bodies, globals, func signatures, func bodies)
need no changes at all to correctly cover a multi-package program, since
they already iterate "every tree passed in" with no notion of package
grouping.

A package-qualified call (`mathutils.Add(...)`) is recognized in codegen
(`isPackageQualifiedCall`, `src/codegen/expr.go`) the same way sema
recognizes it (`Info.Refs` on the callee `MemberExpr`'s own object resolving
to `sema.SymPackage`) and lowered as a **plain direct call** - `genFuncCall`,
the exact same lowering an ordinary same-package free-function call already
uses - since a package-qualified function call has no receiver to compute,
unlike an ordinary method call (`genMethodCall`), which the dispatch in
`genCallExpr` still falls through to otherwise.

**Diamond dependencies are deduped by directory identity**, not re-lowered
per import edge: `loader.LoadProgram` loads a given package directory
exactly once regardless of how many other packages import it (see
`src/loader`'s own doc comment), so its trees appear exactly once in the
flattened list `GeneratePackage` receives - its functions/structs are
declared into the shared Module exactly once, and every importer's calls
into it resolve to that one declaration via the same pointer-keyed maps
above. There is no separate-compilation/linking concept for this project to
get right here at all (see `DECISIONS.md`).

# The `llvmc` CLI driver (`cmd/llvmc`)

The first way to actually *run* an llvm_lang program as a human, rather than
only proving the pipeline works via `go test`: given a path (a single source
file, or a directory - see `LANGUAGE.md`'s "Multi-file packages" section),
it resolves the whole program's transitive import graph
(`src/loader`'s `LoadProgram`, backed by `afero.NewOsFs()` - see
`LANGUAGE.md`'s "Imports" section for the path-resolution/dedup/cycle rules
this implements, and `src/loader`'s own package doc comment for why file
*parsing* now lives there too, not just discovery), drives every package's
files through the rest of the pipeline (`sema.ResolveProgram` ->
`sema.CheckProgram` -> `codegen.GeneratePackage` across the whole program's
trees flattened together - `compileAndRunProgram`/`runPipeline`), and on
full success JIT-executes the resulting module's `main` directly in this
process - so the program's own `print` calls (real libc `printf` calls under
the hood) write to this process's real stdout, which a `go test`-hosted JIT
call can't easily show.

A single-package, single-file program (a directory containing exactly one
`.llx` file, or a file whose sibling directory has no other `.llx` files,
with no `import` declarations at all) goes through this exact same path -
there's no separate single-file/single-package code path in `llvmc` itself,
only `compileAndRun`/`compileAndRunPackage` (used by this package's own
in-process tests that build source strings directly, with no real
file/directory on disk and so no need to go through `loader.LoadProgram` at
all) staying as thin wrappers that call the same shared `runPipeline` tail
`compileAndRunProgram` does, just fed by `sema.ResolvePackage` instead of
`sema.ResolveProgram` (and so with no cross-package export enforcement to
do - `runPipeline`'s own `treePackage` argument is simply nil in that case,
see `sema.CheckProgram`'s doc comment) - the same relationship
`codegen.Generate`/`sema.Resolve`/`sema.Check` each have to their own
multi-file counterpart, one level further up.

## Building and running

```powershell
$mingw = "C:\msys64\mingw64\bin"
if ($env:Path -notlike "*$mingw*") { $env:Path = "$mingw;$env:Path" }
go build -tags=llvm22 -o llvmc.exe ./cmd/llvmc

.\llvmc.exe path\to\program.llx     # a single file - its containing
                                    # directory's other .llx files (if any)
                                    # are part of the same package too
.\llvmc.exe path\to\a\directory     # every .llx file directly in it
```

## The `-emit-llvm` flag

```powershell
.\llvmc.exe -emit-llvm path\to\program.llx
```

Runs the exact same pipeline - including LLVM's own module verifier - but
instead of JIT-executing the result, prints the generated module's LLVM IR
text (`Module.LLVM.String()`, the same method the repo root `main.go` smoke
test already uses) to stdout and exits 0, without ever executing anything.
This is purely additive: no flag keeps the default JIT-execution behavior
exactly as before. It's meant for debugging a language feature's codegen
lowering without writing a throwaway `go test` every time.

```powershell
PS> .\llvmc.exe -emit-llvm .\examples\hello.llx
; ModuleID = '.\examples\hello.llx'
source_filename = ".\\examples\\hello.llx"
...
@.str.0 = private unnamed_addr constant [14 x i8] c"Hello, World!\00"
...
define i32 @main() {
entry:
  %0 = call i32 (ptr, ...) @printf(ptr @.fmt.str, i32 13, ptr @.str.0)
  ret i32 0
}
PS> echo $LASTEXITCODE
0
```

Note the source string still shows up as module-level constant data
(`@.str.0`) even here - every string literal `codegen` sees gets embedded as
a global regardless of whether `main` is ever actually called (see the
"`string` representation" section above) - but `main`'s body is never run,
so `printf` never actually fires and nothing is written to the real stdout
beyond the IR text itself.

Since this path never reaches `llvm.NewExecutionEngine`, disposal is a plain
`Module.Dispose()` - same as the diagnostic/verification-failure paths below,
not the JIT path's more careful engine/context teardown.

## Source file extension: `.llx`

This project's source files use the extension `.llx`, not `.ll` - `.ll` is
already LLVM's own textual IR format's extension, and reusing it here would
be a real (and confusing) collision with that, since this compiler also
prints/inspects real LLVM IR (`Module.LLVM.String()`) elsewhere. The
compiler pipeline proper still doesn't inspect a file's extension at all -
`lexer.NewFile` just takes a name (used only for diagnostics) and the source
text, exactly as before - `src/loader`'s directory scan is the one place
`.llx` is now checked for real (case-insensitively), since "every `.llx`
file directly in this directory" (see `LANGUAGE.md`'s "Multi-file packages"
section) has to mean *something* concrete when resolving a bare directory
path rather than one named file. See `examples/` at the repo root for sample
`.llx` programs, each now its own subdirectory so a single-file example
doesn't accidentally pull its siblings into the same package (`hello/`,
a struct+method+loop+arithmetic program in `features/`, a deliberately
invalid program in `error/` demonstrating the failure path, and
`multifile/` - three files demonstrating cross-file calls, see that
directory's own doc comments).

## Exit codes

- **2** - a usage error: no path argument, an unrecognized flag, the path
  couldn't be resolved to a real file/directory, its resolved directory has
  zero `.llx` files in it, an imported package directory couldn't be found,
  or a real import cycle was detected (see `src/loader`'s `Load`/
  `LoadProgram`). A short message goes to stderr; nothing is compiled.
- **1** - a compile-time diagnostic from the lexer, parser, `sema.Resolve`,
  `sema.Check`, or `codegen.Generate` stage (the pipeline stops at the first
  stage reporting an error-severity diagnostic, exactly like every other
  driver of this pipeline in this codebase) - or the module failing LLVM's
  own verifier, or a module with no `main` function to JIT-execute. Every
  diagnostic from whichever stage failed is printed to stderr via
  `diag.FormatSnippet` (a `file:line:col: severity: message` header plus the
  offending source line and a caret). With `-emit-llvm`, this is the only
  non-zero exit code reachable at all - a verified module's IR is always
  printed and the process always exits 0 afterward (see below).
- **otherwise** - the language program's own exit code. `func main()` always
  lowers to a real, parameterless `i32 @main()` regardless of whether the
  source declares a return type for it (see the "`main` is the real entry
  point" section above) - falling off the end or a bare `return` becomes
  `ret i32 0`, and `return expr` returns `expr` directly (`int` is `i32`, so
  no truncation/cast is ever needed either way - see "`int` is 32-bit"
  above). `llvmc` propagates that i32 result directly as its own process
  exit code, so `func main() int { return 2 + 3 }` really does exit the
  `llvmc` process with code 5. This doesn't apply with `-emit-llvm`: `main`
  is never executed, so its return value never becomes the process's exit
  code - see the "`-emit-llvm` flag" section above.

## A non-obvious disposal detail

Once a `codegen.Module`'s `LLVM` field is handed to
`llvm.NewExecutionEngine`, the engine takes ownership of it - calling
`Module.Dispose()` afterward would double-free it (this exact pitfall is
already documented on `src/codegen/codegen_test.go`'s `compileAndJIT`
helper). So `llvmc` only calls `Module.Dispose()` on the paths that never
reach a live execution engine (a codegen diagnostic, or a failed
`llvm.VerifyModule`); once JIT execution is about to happen, disposal goes
through the engine (`engine.Dispose()`) and then the module's owning
`Context` (`mod.Ctx.Dispose()`), in that order, instead.

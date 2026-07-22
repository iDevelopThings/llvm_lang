# LLVM Go Custom Language

Hobby project to create a custom programming language using LLVM and Go.

We're using Go 1.26, so **ALWAYS** opt for the latest language features.

# Compiling

Because of llvm support, we need cgo.
There is a build.ps1 script you can use to compile.

# Project Info

## Enums

Enums are code generated using go generate, you can find existing yml defs inside src/enums.
It's preferable to use the enum gen rather than go style enums, the generator is flexible, lets us add extra fields, creates maps we need, provides extra helper functions for the type etc.

## Standards

Max efficiency and performance, keeping allocations down to a minimum. We already have cgo overhead, so we must avoid adding anymore.

When the choice is right, opt for using iter.Seq yield feature, slices.X from std library etc.

## Architecture

All code should be at it's correct layer, for example no code gen logic should be inside type checker(sema), and vice versa. (This applies everywhere, not just these two examples).
It's important to maintain and embrace this, or the project will quickly become a tangled mess of unmaintainable and difficult code.


## Project Code Style Preferences **IMPORTANT**

Avoid single line syntax, for ex:

Bad:
```go
return Token{Lexeme: enums.Lexemes.Semicolon, Start: Pos(l.pos), End: Pos(l.pos)}
```
Good:
```go
return Token{
    Lexeme: enums.Lexemes.Semicolon,
    Start: Pos(l.pos),
    End: Pos(l.pos),
}
```

### Switches:
Bad:
```go
switch t.Lexeme {
	case enums.Lexemes.Number, enums.Lexemes.String, 
	enums.Lexemes.RightParen, enums.Lexemes.LeftParen, enums.Lexemes.Identifier:
        return true
    case enums.Lexemes.Identifier:
        return true
    default:
        return false
}
```
Good:
```go
switch t.Lexeme {
	case enums.Lexemes.Number,
		enums.Lexemes.String,
		enums.Lexemes.RightParen,
		enums.Lexemes.RightBracket,
		enums.Lexemes.RightBrace,
		enums.Lexemes.PlusPlus,
		enums.Lexemes.MinusMinus:
        return true
    case enums.Lexemes.Identifier:
        return true
    default:
        return false
}
```



## Parser

`src/parser` is split by concern, not dumped into one file:
- `parser.go` - core scaffolding only: token cursor, expect/accept/sync, diagnostics, bailout/Run. No grammar rules belong here.
- `expr.go` - expression parsing.
- `stmt.go` - statement parsing.
- `decl.go` - top-level declarations.

Expression parsing uses a **Pratt parser** (operator-precedence parsing via a table keyed by `enums.Lexeme`, mapping to prefix/infix parse functions plus a precedence) - not a hand-cranked function per precedence level. Postfix operators (call `(`, index `[`, member `.`) are registered as infix table entries at the highest precedence, so they chain through the same loop as binary operators instead of a separate bolted-on postfix phase.

This area needs strong test coverage as it grows - precedence/associativity bugs are easy to introduce and easy to miss without tests backing every rule. Prefer table-driven tests asserting `Tree.Dump()` output for precedence/associativity/chaining cases.

# Language Syntax

The language is supposed to be similar to go's syntax.

## Top level

File scope is Go-style, not script-style: only `var`, `func`, and `struct` declarations are legal directly at the top level - no bare `if`/`for`/`:=`/expression-statements there. This isn't an arbitrary restriction: LLVM has no notion of "just run a statement at global scope," only static data initializers, so a top-level `var` is a real global (no function needed to "run" it) while anything actually executable needs a real entry point, same as Go/C/Rust - `func main()` is required for that.

```go

var a int = 5
var b int = 10
var s string = "Hello, World!"
var n bool = true

func add(x int, y int) int {
    return x + y
}

func main() {
    c := a + b

    if c >= 10: print("....")

    if c >= 10 {
        print("....")
    } else {
        print("....")
    }
}

```

## Loops

Only `for` exists - no `while`. It covers all three Go-style forms:

```go
for i := 0; i < 10; i++ {
    print(i)
}

for c < 10 {
    c = c + 1
}

for {
    break
}
```

## Arrays

Go-style prefix type syntax: `[N]T` (fixed-size) and `[]T` (dynamic/slice).

Fixed-size arrays are implemented now - a plain LLVM array type, no runtime needed. Dynamic slices are parsed (so the grammar doesn't need to change later) but rejected at a later stage for now - they need heap allocation/an allocator, which is an investigation for after the LLVM lowering stage exists.

```go
var a [5]int
a[0] = 1

b := [3]int{1, 2, 3}
```

## Structs

Data-only declarations - no nested methods:

```go
struct Point {
    x int
    y int
}
```

Methods are declared separately, Go-receiver-style but without naming the receiver - refer to it as `this`. Every method is implicitly by-reference, so there's no value-vs-pointer receiver distinction to reason about:

```go
func (Point) move(dx int, dy int) {
    this.x = this.x + dx
    this.y = this.y + dy
}
```

## Assignment

`=` assigns to any lvalue: an identifier, `.field`, or `[index]`. Compound assignment and increment/decrement are supported: `+=  -=  *=  /=  ++  --`.

```go
a = 5
p.x = 10
arr[0] = 1
x += 1
x++
```

## Functions

`return` supports a single value only - no `return a, b` tuple returns, at least for now.

## Types

Primitive types: signed integers `i8`, `i16`, `i32`, `i64`; floats `f32`,
`f64`; `string`; `bool`. `int` is not a separate type - it's exactly a
synonym for `i32` (see `sema.TypeInt`'s doc comment: both spellings produce
the literal same `Type` value, so `var a int = 1` and `var b i32 = a` need no
conversion between them at all). A named struct type and an array type
(`[N]T` fixed-size, `[]T` dynamic) round out the type system - see their own
sections above.

There is still no *implicit* conversion between two named types anywhere:
every assignment, call argument, return value, and operator operand must
match types exactly (`Equal`, sema/types.go) or it's a compile error - two
already-concretely-typed values of different widths (`i32` and `i64`, say)
cannot mix without an explicit conversion (see "Explicit conversions"
below), even though both are perfectly valid types individually:

```go
var a i32 = 1
var b i64 = 2
c := a + b        // error: operator + not defined for int and i64
d := a + i64(1)   // fine - i64(1) is int64 already; a's own int-ness must
                   // still be converted the same way for a truly mixed sum
```

### Untyped numeric constants

The one deliberate exception to "no implicit conversion" is Go's own
"untyped constant" model, narrowed to numeric literals (`sema.TypeUntypedInt`
/ `TypeUntypedFloat`, `sema/types.go`) - `5`/`5.0` are not immediately `i32`/
`f64`, they start out *untyped* and only become a concrete type once some
surrounding context pins one down:

- **A declared type** - a `var`'s annotation, a function's declared return
  type, a parameter's declared type, a composite-literal element's expected
  type - the untyped constant simply becomes that type, provided it's
  numeric and the adaptation doesn't silently lose information:

  ```go
  var a i64 = 5      // untyped int 5 becomes i64
  var b f32 = 5      // untyped int adapting to a float context is fine
  var c f64 = 5.5    // untyped float 5.5 becomes f64

  var d int = 5.5    // error: cannot use untyped float as int in variable
                      // declaration (would truncate) - same as Go rejecting
                      // this exact case
  ```

- **A concretely-typed operand in a binary expression** - the untyped side
  adopts the other side's type (again, untyped-float -> int is rejected):

  ```go
  var a i64 = 1
  b := a + 5          // untyped 5 becomes i64, matching a
  c := a + 5.5         // error: untyped float can't adapt to a's int context
  ```

- **Two untyped operands combining** - the result itself stays untyped,
  deferring resolution further up the tree; it becomes untyped-*float* if
  either side looks like a float, otherwise untyped-int:

  ```go
  var a i8 = 1 + 2     // both untyped-int; combined stays untyped, then
                        // adapts fine to i8
  var b f32 = 1 + 2.5  // combined is untyped-float (2.5 looks like a float);
                        // adapts fine to f32
  var c i32 = 1 + 2.5  // error: untyped float can't adapt to an int context
  ```

- **No declared type at all** (`var a = 5`, or `a := 5`) - Go's own
  untyped-constant *default* applies: untyped int defaults to `i32`,
  untyped float defaults to `f64`.

  ```go
  a := 5      // a is i32
  b := 5.0    // b is f64
  ```

A comparison (`== != < <= > >=`) is a dead end for this deferral - its own
result is always `bool`, never numeric, so there's nowhere further up the
tree to defer to. Two untyped operands compared directly (`1 < 2`) default
immediately, right there, the same way a type-less `var` would.

bool/string literals never need any of this - each has exactly one
representation, so there's no multiple-concrete-type ambiguity for context
to resolve.

## Explicit conversions

`T(x)` performs an explicit numeric conversion - any int width to any other
int width or float width, and vice versa (`i8`/`i16`/`i32`/`i64`/`f32`/`f64`,
any pairing). This reuses the ordinary `CallExpr` grammar entirely - a type
name in type position is already just an `Ident` node, so `i64(x)` parses
identically to a function call `f(x)`; no parser change was needed for this
feature at all. `sema.Check` recognizes it the moment a `CallExpr`'s callee
identifier resolves (via `Info.Refs`, the same lexical resolution every other
identifier already goes through) to a *type* symbol rather than a function -
see `checkConversionCall`, `src/sema/typecheck.go`.

```go
var a i32 = 5
b := i64(a)     // sign-extend i32 -> i64
c := i8(b)      // truncate i64 -> i8
d := f64(a)     // int -> float (sitofp)
e := i32(d)     // float -> int (fptosi, truncates toward zero)
f := f32(d)     // float -> narrower float (fptrunc)
g := i32(a)     // same type - passes the value through unchanged, no
                // instruction emitted at all (see codegen's genConversion)
```

Scoped to **numeric-to-numeric only** - a conversion whose target or
argument isn't numeric (`i64("hello")`, `Point(x)`, `bool(x)`, `string(x)`)
is rejected: `string`/`struct`/`array`/`bool` conversions aren't meaningfully
"conversions" in the C-cast sense this feature covers, and are out of scope.
A wrong argument count (`i64(1, 2)` or `i64()`) is also a real error
("conversion to i64 requires exactly one argument, got N"). The conversion's
own target `Type` is recorded as the `CallExpr` node's own type in
`Info.Types`, exactly like any other expression - codegen recognizes the
same node the same way sema did (`Info.Refs` on the callee resolving to a
type symbol), not a separate mechanism.

Unlike an *implicit* untyped-constant adaptation (see above), an explicit
conversion allows the one direction that would otherwise be rejected as
lossy: `i32(5.5)` (float -> int) truncates deliberately, same as Go's own
`int(5.5)`.

## Operators

- `+` is overloaded twice: `numeric + numeric -> numeric` (arithmetic, any
  combination of int/float widths that resolves to a common type - see
  "Untyped numeric constants" above) and `string + string -> string`
  (concatenation). No other operator works on `string`.
- `- * /` are `numeric + numeric -> numeric` - int widths and floats alike
  (`f32`/`f64` lower to real floating-point instructions - `CreateFAdd`/
  `CreateFSub`/`CreateFMul`/`CreateFDiv` - not integer ones).
- `%` and the bitwise `& | ^` are `integer + integer -> integer` only - any
  int width, but never float, same restriction Go itself has.
- `== !=` require both operands to already be the same type (numeric of any
  matching width/kind, `string`, `bool`, or the exact same struct/array type
  - `Type.Equal`, `sema/types.go`) and produce `bool`. Two structs are equal
  iff every corresponding field is equal, recursively (a field can itself be
  a struct or array); two arrays are equal iff every corresponding element is
  equal, recursively - the same rule Go itself uses. Comparing two
  *different* struct/array types (or a struct against an array) remains a
  compile error, same as every other operator here - no implicit conversion
  anywhere in this language. A float comparison lowers to `FCmp` with the
  *ordered* `OEQ`/*unordered* `UNE` predicates (`==`/`!=` respectively) -
  `==` is false and `!=` is true whenever either operand is NaN, matching
  Go's own float-equality semantics.

  ```go
  struct Point {
      x int
      y int
  }

  a := Point{1, 2}
  b := Point{1, 2}
  a == b            // true - both fields equal
  a != Point{1, 3}  // true - y differs

  [3]int{1, 2, 3} == [3]int{1, 2, 3}  // true
  [3]int{1, 2, 3} == [3]int{1, 2, 9}  // false
  ```
- `< <= > >=` are `numeric + numeric -> bool` or `string + string -> bool`
  (real byte-by-byte lexicographic comparison of the strings' content, same
  as Go) - no other type works with them. A float ordering comparison lowers
  to `FCmp` with the *ordered* predicates (`OLT`/`OLE`/`OGT`/`OGE`) - false
  whenever either operand is NaN, again matching Go.
- `&& ||` are `bool + bool -> bool` only (no truthy/falsy coercion of other
  types).
- Unary `-` is `numeric -> numeric` (any int width or float width - `f32`/
  `f64` lower to `CreateFNeg`, not `CreateNeg`); unary `!` is `bool -> bool`.
- `++`/`--` and compound assignment (`+= -= *= /=`) apply the matching
  operator's rule to the target's and value's types, generalized the same
  way as the corresponding standalone operator - `+=` also accepts `string`,
  same as `+`; the rest work on any numeric width or float, dispatching to
  the matching float instruction whenever the target itself is `f32`/`f64`.

## Array sizes

`[N]T`'s size `N` must be a literal integer constant (a bare `NumberLit`
node) - there's no constant-expression evaluator yet, so `var n int = 3;
var a [n]int` is rejected ("array size must be a constant integer
literal"), even though `n` never changes. `N` must also be a positive
literal (`[0]T` and negative sizes are rejected).

Array composite literals (`[N]T{...}`) only support positional elements
(`[3]int{1, 2, 3}`) - Go's index-keyed array literal form (`[5]int{2: 9}`)
isn't supported.

## The `print` builtin

`print` is predeclared (see `sema.universeScope`), not a real function
declaration, so it has no parameter list to check calls against. It accepts
exactly one argument, of any type, and returns nothing (`void`).

## Missing return

A function declaring a return type must be guaranteed to return a value on
every possible execution path - `sema.Check` runs a full flow analysis for
this (`isTerminatingStmt`, `src/sema/typecheck.go`), modeled directly on Go's
own spec ("Terminating statements"), cut down to this language's smaller
statement grammar (no goto/labels/switch/select/panic exist here). A
statement list ends in a terminating statement if its last statement does,
where a terminating statement is one of:

- a `return`.
- an infinite `for {}` (no `cond` clause) with no `break` that targets it
  directly - a nested loop's own `break` doesn't count. A `for` **with** a
  `cond` clause is never terminating: the condition can always be false and
  fall through.
- an `if`/`else` where **both** branches are present and both themselves end
  in a terminating statement. An `if` with no `else` - including the
  one-line `if cond: stmt` form, which is grammatically identical (see
  ast.Node's IfStmt doc comment) - can never be terminating: there's always
  a path where the condition is false and control falls straight through.
- a `Block` whose own last statement is terminating.

A function declaring a return type whose body isn't terminating gets a real
diagnostic, "missing return" (matching Go's own wording). A function
declaring no return type needs no such check - falling off its end is
legitimate, same as Go.

```go
func f() int {
    if true {
        return 1
    }
    // error: missing return - the false path falls through with nothing
}

func g() int {
    if true {
        return 1
    } else {
        return 2
    }
    // fine - every path returns
}

func h() int {
    for {
        // fine - infinite loop, no break targeting it, never falls through
    }
}
```

See `src/codegen/func.go`'s `emitFallbackTerminator` for why codegen still
keeps its own `unreachable` fallback anyway, as a defensive backstop, even
though a validated tree should never actually need it now.

## No first-class functions

A function or method name is not a value anywhere in this language -
`parseTypeExpr` has no function-type syntax, so a variable could never be
declared to hold one. `add` (referencing a function without calling it) and
`p.move` (referencing a method without calling it) are both compile errors
("is a function/method, not a value"); only `add(...)`/`p.move(...)` - an
actual call - is meaningful. Passing a function to another function, storing
one in a struct field, or returning one are all therefore not expressible
yet, not just unchecked.

# Codegen (`src/codegen`)

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
and the platform C ABI's `int` are simply the same type. See BLOCKERS.md.

## Numeric type -> LLVM type, and where the type resolution itself lives

`sema.Type`'s six concrete numeric kinds map straight onto go-llvm's own
integer/float constructors (`llvmType`, `src/codegen/types.go`): `i8`/`i16`/
`i32`/`i64` -> `Int8Type`/`Int16Type`/`Int32Type`/`Int64Type`; `f32`/`f64` ->
`FloatType`/`DoubleType`. An LLVM integer/float instruction (`CreateAdd`,
`CreateICmp`, ...) is already generic over bit width as long as both
operands share the same LLVM type - which sema guarantees by construction
(see the Types section above) - so no per-width branching is needed inside
`genBinaryExpr`/`genUnaryExpr`/etc.; only the *kind* (integer vs. float)
needs to be checked, to pick the matching real floating-point instruction
(`CreateFAdd`/`CreateFSub`/`CreateFMul`/`CreateFDiv`/`CreateFCmp`/
`CreateFNeg`) instead of the integer one.

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
directly to the `Info.Types` fix instead).

## Explicit conversions, concretely

`T(x)` (see the language-level Types/Explicit-conversions sections above) is
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
byte for byte - something no prior round of this project had a way to do
(see BLOCKERS.md's codegen-phase entry #7). `TestPrintI64FormatSpecifierIsCorrect`/
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
(so `"ab" < "abc"`, matching Go). See BLOCKERS.md.

## The arena allocator (`src/codegen/runtime.go`'s `setupArena`)

Every codegen-level heap allocation goes through one centralized bump
allocator instead of calling libc `malloc` directly at each call site -
currently that's just string concatenation (`genStringConcat`), but any
future heap-needing feature (e.g. dynamic arrays, if/when they're designed)
should route through this same primitive rather than reintroducing scattered
`malloc` calls.

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
instead of ad hoc `malloc` calls at each use site. See BLOCKERS.md: a real
memory-management strategy (actual scoped frees, refcounting, a real GC) is
still an open, deliberately-deferred question for the user to decide - this
arena is groundwork for that future decision, not an attempt to answer it.

## Array bounds checking

Indexing a fixed-size array - both a read (`a[i]`) and a store
(`a[i] = v`) - lowers to a real runtime check, not a bare GEP: `i < 0 || i >=
size` (`size` is always known at compile time - see the "Array sizes"
section above) traps immediately via LLVM's `llvm.trap` intrinsic (declared
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
See BLOCKERS.md for why this fork was made explicitly rather than inferred.

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

A method's receiver (see the Structs section above - always implicit,
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
this doc's "Missing return" section) and rejects any function declaring a
return type whose body isn't guaranteed to return. Codegen still keeps a
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

# The `llvmc` CLI driver (`cmd/llvmc`)

The first way to actually *run* an llvm_lang program as a human, rather than
only proving the pipeline works via `go test`: it reads a source file, drives
it through the full pipeline (`lexer.NewFile` -> `parser.ParseFile` ->
`sema.Resolve` -> `sema.Check` -> `codegen.Generate`), and on full success
JIT-executes the resulting module's `main` directly in this process - so the
program's own `print` calls (real libc `printf` calls under the hood) write
to this process's real stdout, which a `go test`-hosted JIT call can't
easily show (see BLOCKERS.md's codegen-phase entry 7).

## Building and running

```powershell
$mingw = "C:\msys64\mingw64\bin"
if ($env:Path -notlike "*$mingw*") { $env:Path = "$mingw;$env:Path" }
go build -tags=llvm22 -o llvmc.exe ./cmd/llvmc

.\llvmc.exe path\to\program.llx
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
a global regardless of whether `main` is ever actually called (see "`string`
representation" above) - but `main`'s body is never run, so `printf` never
actually fires and nothing is written to the real stdout beyond the IR text
itself.

Since this path never reaches `llvm.NewExecutionEngine`, disposal is a plain
`Module.Dispose()` - same as the diagnostic/verification-failure paths below,
not the JIT path's more careful engine/context teardown.

## Source file extension: `.llx`

This project's source files use the extension `.llx`, not `.ll` - `.ll` is
already LLVM's own textual IR format's extension, and reusing it here would
be a real (and confusing) collision with that, since this compiler also
prints/inspects real LLVM IR (`Module.LLVM.String()`) elsewhere. Nothing in
the compiler pipeline itself actually inspects a file's extension -
`lexer.NewFile` just takes a name (used only for diagnostics) and the source
text - so this is purely a human-facing convention, not an enforced one. See
`examples/` at the repo root for sample `.llx` programs (`hello.llx`, a
struct+method+loop+arithmetic program in `features.llx`, and a deliberately
invalid program in `error.llx` demonstrating the failure path).

## Exit codes

- **2** - a usage error: no file argument, an unrecognized flag, or the file
  couldn't be read. A short usage message goes to stderr; nothing is
  compiled.
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

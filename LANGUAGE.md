# llvm_lang Language Reference

The syntax and semantics of the language itself - what a program written in
it looks like and how it behaves. This is separate from `AGENTS.md` (repo
conventions/how to work in this codebase) and `CODEGEN.md` (how the compiler
lowers the constructs described here into LLVM IR) so that a task touching
only one layer doesn't have to load the others. See `AGENTS.md` for that
split and pointers to the rest.

The language is supposed to be similar to go's syntax.

## Top level

File scope is Go-style, not script-style: only `import`, `var`, `func`, and `struct` declarations are legal directly at the top level - no bare `if`/`for`/`:=`/expression-statements there. This isn't an arbitrary restriction: LLVM has no notion of "just run a statement at global scope," only static data initializers, so a top-level `var` is a real global (no function needed to "run" it) while anything actually executable needs a real entry point, same as Go/C/Rust - `func main()` is required for that. See "Imports" below for `import`'s own rules (it must come first in a file, before any other declaration).

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
conversion between them at all - see `DECISIONS.md` for why this width was
chosen). A named struct type and an array type (`[N]T` fixed-size, `[]T`
dynamic) round out the type system - see their own sections above.

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
exactly one argument, of any type, and returns nothing (`void`). See
`CODEGEN.md`'s "`print` builtin, concretely" section for how it renders each
type at runtime.

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

See `src/codegen/func.go`'s `emitFallbackTerminator` (`CODEGEN.md`'s
"Terminator safety" section) for why codegen still keeps its own
`unreachable` fallback anyway, as a defensive backstop, even though a
validated tree should never actually need it now.

## First-class functions

A **free function's** name is a value now, not just something you call:
`add` (referencing a function without calling it) has the type `func(int,
int) int` and can be assigned to a matching `var`, passed as an argument, or
returned from another function - the same `checkAssignable` path every other
type in this language already goes through, generalized. Calling it is still
spelled the same way as ever (`add(1, 2)`) - a call and a bare reference are
just two different uses of the identical `add` expression, disambiguated the
same way everything else in this language's grammar is: whether it's
immediately followed by `(`.

```go
func add(x int, y int) int {
    return x + y
}

func apply(fn func(int, int) int, x int, y int) int {
    return fn(x, y)
}

func getAdd() func(int, int) int {
    return add
}

func main() {
    fn := add              // fn's type is func(int, int) int
    r1 := fn(1, 2)          // called indirectly through the variable
    r2 := apply(add, 3, 4)  // passed as an argument, called inside apply
    r3 := getAdd()(5, 6)    // returned from a function, then called
}
```

A method value (`p.move`, referenced without a call) remains **out of
scope** and still a compile error, exactly as before this round - only a
method *call* (`p.move(...)`) is meaningful. This is a deliberate, narrower
scope than free functions get: a method value would need to close over its
receiver (`this`) somehow, which is a real design question of its own, not
yet answered - see `DECISIONS.md`'s "First-class functions" entry for how
the representation below was chosen to leave room for that future decision
without a redesign.

### The function-type grammar: `func(T1, T2) R`

A function type is written `func(` followed by a comma-separated list of
parameter *types* (no parameter names - just like a function type has never
needed argument names in Go either) `)`, then an optional return type - the
same "return type is optional, meaning void" rule `FuncDecl` itself already
has (`func(int, int)` is a valid function type, over a function taking two
ints and returning nothing). It appears anywhere any other type can: a
`var`'s annotation, a parameter's type, a struct field's type, an array
element type, or nested inside another function type (a function type can
take or return another function type - `func(func(int) int) int` parses and
type-checks the same way `[5][3]int` does for arrays).

See `CODEGEN.md`'s "First-class functions" section for how a function value
actually lowers to LLVM IR (the fat-pointer representation, and the direct-
vs-indirect call distinction) - that's an implementation concern, not a
language-spec one, so it lives there instead of here.

## Multi-file packages

A package is a directory of `.llx` files, Go-style: every `.llx` file
directly inside one directory merges into a single shared scope, exactly as
if their contents had all been concatenated into one file - a function,
`var`, or `struct` declared in one file is visible and callable from every
other file in the same directory, regardless of which file declares it
first. Declaration order across files never matters, matching the existing
same-file guarantee: a file processed later in a package can still be called
from a file processed earlier, the same way two declarations later in a
single file can already reference each other regardless of order.

```
myprogram/
    main.llx     // func main() { print(double(21)) }
    helper.llx   // func double(x int) int { return x * 2 }
```

Both of the following compile the identical package - a bare directory, or
any one file inside it, both resolve to "every `.llx` file directly in that
directory":

```powershell
llvmc myprogram
llvmc myprogram/main.llx
```

**Non-recursive:** only the `.llx` files directly inside the given
directory are part of the package - a subdirectory is never walked into,
even if it also contains `.llx` files. A subdirectory full of `.llx` files
is simply a separate, unrelated package by this round's rules (there's no
way yet to reference it from another package at all - see below).

**Cross-package imports:** this section covers single-package scoping only;
see "Imports" below for real `import` syntax, cross-package resolution, and
cycle detection, all built directly on top of the directory/multi-file model
described here.

**`Exported`/visibility is now a real, enforced rule:** Go's own name-case
visibility convention (`Point` visible outside its package, `point` not) is
implemented via `sema.Symbol.Exported` (see `sema/scope.go`) and actually
enforced the moment a second package exists to enforce it against - see
"Imports" below for exactly what's checked and what isn't. **Within a single
package, case still never matters for visibility** - every example above,
and every same-package cross-file reference, is completely unaffected by
this: `Exported` only has any effect the moment a name is reached through an
actual package qualifier (`pkg.Name`).

## Imports

A package can reference another package's **exported** (capitalized-name)
top-level declarations via a new top-level `import "path"` declaration,
which must come before every other top-level declaration in the file
(matching Go's own ordering rule - simplest to parse, no real downside):

```go
// app/main.llx
import "./mathutils"

func main() int {
    return mathutils.Add(1, 2)
}

// app/mathutils/add.llx  (package "mathutils", i.e. the directory name)
func Add(a int, b int) int {
    return a + b
}
```

**Path resolution is relative to the importing file's own directory** - not
the entry package's directory, and not a module-root/manifest scheme (see
`DECISIONS.md`): `./mathutils` written in `app/main.llx` resolves to
`app/mathutils`, exactly the same directory-resolution `src/loader` already
does for a single root, extended to follow every import transitively,
deduping a diamond dependency (two packages importing the same third one) by
directory identity so it's only ever loaded once, and rejecting a real
import cycle with a clear error naming it (e.g.
`import cycle: a -> b -> a`) rather than looping forever.

**An import's local name** defaults to its path's own last segment -
`mathutils` for `./mathutils`, `util` for `../shared/util` - Go's own
convention. **There is no aliasing syntax yet** (`import m "./mathutils"`) -
deliberately deferred (see `DECISIONS.md`): easy to add later, not needed
for this round to be complete.

**An import binding is file-scoped, not package-scoped** - matching Go
exactly. An import declared in one file of a package is *not* visible in a
different file of the same package unless that file also writes its own
`import "./mathutils"`:

```go
// app/main.llx
import "./mathutils"

func useIt() int {
    return mathutils.Add(1, 2)   // fine - this file imported it
}

// app/other.llx  (same package as main.llx)
func alsoUseIt() int {
    return mathutils.Add(3, 4)   // error: undefined: mathutils - this file
                                 // never wrote its own import
}
```

**Export enforcement is real:** referencing an unexported name through a
package qualifier is a compile error - this applies to top-level functions,
struct types, and a struct value's fields/methods, whenever the struct type
itself belongs to another package:

```go
// mathutils/add.llx
func Add(a int, b int) int { return a + b }    // exported - reachable
func double(x int) int { return x * 2 }        // unexported - NOT reachable

struct Point {
    X int      // exported field
    secret int // unexported field
}

// app/main.llx
import "./mathutils"

func main() int {
    mathutils.Add(1, 2)      // fine
    mathutils.double(1)      // error: mathutils.double is not exported

    p := mathutils.Point{X: 1}      // fine - a keyed literal may simply omit
                                     // a field it doesn't name
    q := mathutils.Point{1, 2}      // error: cannot use a positional literal
                                     // to construct Point from another
                                     // package: field secret is unexported
    r := mathutils.Point{X: 1, secret: 2} // error: secret is not exported

    _ = p.X                  // fine
    _ = p.secret             // error: secret is not exported
}
```

Constructing a struct value from another package follows Go's own rule
exactly, not just member *access*: a **positional** composite literal
(`mathutils.Point{1, 2}` above) is rejected outright the moment the struct
has *any* unexported field - even one the literal never mentions by name and
even though every value it does supply is itself fine - because a positional
literal has no way to "skip" a field, so allowing it would silently let
outside code set a private field's value. A **keyed** literal
(`mathutils.Point{X: 1}`) has no such problem and remains fine as long as it
doesn't explicitly name the unexported field itself (`secret: 2` above is
ordinary unexported-access, rejected the same way `p.secret` is).
Same-package construction is completely unaffected either way - export never
matters within one package, regardless of whether the literal is positional
or keyed.

A package-qualified name can also appear in **type position**
(`var p mathutils.Point`, a composite literal's type `mathutils.Point{...}`)
- the exact same export rule applies there too.

**Within one package, nothing here changes:** case still never matters for
same-package visibility (see the "Multi-file packages" section above) -
`Exported`/export enforcement only ever applies to a name reached through an
actual package qualifier.

# llvm_lang Language Reference

The syntax and semantics of the language itself - what a program written in
it looks like and how it behaves. This is separate from `AGENTS.md` (repo
conventions/how to work in this codebase) and `CODEGEN.md` (how the compiler
lowers the constructs described here into LLVM IR) so that a task touching
only one layer doesn't have to load the others. See `AGENTS.md` for that
split and pointers to the rest.

The language is supposed to be similar to go's syntax.

## Top level

File scope is Go-style, not script-style: only `import`, `var`, `func`, `struct`, `enum`, and `tests{}` declarations are legal directly at the top level - no bare `if`/`for`/`:=`/expression-statements there. This isn't an arbitrary restriction: LLVM has no notion of "just run a statement at global scope," only static data initializers, so a top-level `var` is a real global (no function needed to "run" it) while anything actually executable needs a real entry point, same as Go/C/Rust - `func main()` is required for that. See "Imports" below for `import`'s own rules (it must come first in a file, before any other declaration), and "`tests{}`" below for that construct's own rules.

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

## Global `var` initializers

A top-level `var`'s initializer can be any well-typed expression, not just a
literal or another compile-time-constant expression - matching Go's own real
package-level `var` behavior: a function call, a reference to another global,
a `new` heap allocation, a dynamic-array/slice literal, a lambda literal, and
so on all work. There's no `func init() {}` to write by hand for this -
every non-constant global's real initializer runs automatically, once, in a
synthesized routine before `main` ever starts (see `CODEGEN.md`'s "Global
var initializers" section for the actual `@llvm.global_ctors` mechanism this
compiles down to).

```go
func computeStart() int {
    return 40 + 2
}

var start int = computeStart()   // calls a function
var doubled int = start * 2      // reads another global

func main() int {
    return doubled   // 84
}
```

**Initialization order is source declaration order, not a full dependency
graph** - a deliberately narrower simplification than Go's own real spec
(which topologically sorts by each variable's actual dependencies, so which
one is written first in the source doesn't matter there). Here, every
non-constant global's initializer runs strictly in the order it's declared
(across every file in the package, in file-processing order) - a global
whose initializer reads another global declared *later* in the same package
sees only that other global's zero value, not whatever its initializer would
eventually compute:

```go
var a int = b + 1   // b hasn't been initialized yet here - reads b's zero
                     // value (0), so a == 1
var b int = 5        // b's real initializer runs after a's already ran
```

See `DECISIONS.md`'s dated entry for why this round scopes ordering this way
rather than building a real dependency-graph sort up front.

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

`for ... range ...` - iterating a map or array directly, rather than driving
an index by hand - is a fourth, distinct `for` form; see "Range loops" below
(after "Maps"), once both Arrays and Maps have been introduced.

## Arrays

Go-style prefix type syntax: `[N]T` (fixed-size) and `[]T` (dynamic/slice).

Fixed-size arrays are a plain LLVM array type, no runtime needed:

```go
var a [5]int
a[0] = 1

b := [3]int{1, 2, 3}
```

### Dynamic arrays (slices)

`[]T` is a real, working type now - a growable, heap-backed sequence, built
on top of the arena allocator (see `CODEGEN.md`'s "The arena allocator" and
"Dynamic arrays" sections). Three predeclared builtins - `make`, `append`,
and `len` - are how a program actually creates and grows one; there's no
slice literal-with-no-elements shorthand or any other syntax for it.

- **`make([]T, n)`** allocates a fresh, zero-filled backing buffer of length
  `n` (and capacity `n`, since none was given). **`make([]T, n, cap)`**
  allocates capacity `cap` up front instead, with length still `n` - room to
  `append` into without an immediate reallocation. `n`/`cap` are ordinary
  runtime `int` expressions, not required to be a compile-time constant the
  way `[N]T`'s own `N` is (see "Array sizes" below) - that's the entire
  point of "dynamic". `cap < n` (when both are given) is rejected - as a
  runtime trap, the same hard-abort mechanism an out-of-range index already
  uses (see "Array bounds checking" below), not a compile-time diagnostic:
  since `n`/`cap` are arbitrary runtime values, there's no way to reject a
  bad relationship between them until the program actually runs.

  ```go
  a := make([]int, 3)       // len 3, cap 3, all zero
  b := make([]int, 1, 4)    // len 1, cap 4
  ```

- **`append(slice, elem)`** appends exactly one element per call - **not**
  Go's full variadic `append(s, e1, e2, ...)` form. This is a deliberate,
  natural-to-extend-later restriction (see `DECISIONS.md`), not an
  oversight: this language has no variadic functions anywhere yet, and
  inventing that machinery just for `append` was out of scope for this
  round. Appending several elements is just several calls:

  ```go
  s := make([]int, 0)
  s = append(s, 1)
  s = append(s, 2)
  ```

  Growth matches Go's own real semantics, aliasing quirk included: if the
  slice's `len < cap`, the new element is written directly into the
  existing backing buffer and the result reuses the exact same backing
  pointer - mutating in place, so another variable still holding the
  pre-append slice will see the write too, exactly like Go. Only when
  `len == cap` (capacity exhausted, including a zero-capacity slice) does
  `append` allocate a new, larger buffer (doubling - see `DECISIONS.md`),
  copy the existing elements over, and return a slice pointing at the new
  buffer instead - the old one is simply abandoned (this project's arena
  never frees anything - see `CODEGEN.md`).

- **`len(x)`** works on a dynamic array (its runtime length), a fixed-size
  array (its compile-time-known size), or a `string` (its runtime length) -
  matching Go's own `len` working across all three. Any other type (a
  struct, a numeric type, `bool`) is rejected.

**Slice composite literals** (`[]T{1, 2, 3}`, as opposed to only `[N]T{...}`
for a fixed-size array) also work, for consistency - sugar that allocates a
properly sized backing buffer (via the same arena path `make` itself uses)
and fills it positionally, with the resulting slice's `len` and `cap` both
equal to however many elements the literal lists. Same restriction as a
fixed-size array literal: positional elements only, no keyed form.

```go
s := []int{1, 2, 3}   // len 3, cap 3
```

**Slices are not comparable.** `==`/`!=` between two dynamic-array-typed
operands is rejected outright, mirroring Go's own restriction exactly (Go
only allows comparing a slice against `nil`, a concept this language doesn't
have yet) - there's no slice equality semantics invented here that Go itself
doesn't have.

## Slicing

Go-style slice expressions: `s[a:b]`, `s[:b]` (low bound omitted, defaults to
`0`), `s[a:]` (high bound omitted, defaults to the operand's own length), and
`s[:]` (both omitted). A slice expression produces a **new header value
sharing the same backing memory** as the original - no copy - matching Go's
own real slicing semantics exactly: mutating through the result is visible
through the original, and vice versa. This deliberately doesn't cover Go's
less-common 3-index form (`s[a:b:c]`) - not needed, out of scope.

Three different operand types share this one grammar, each with its own
result type and its own bounds rule:

- **A dynamic array (`[]T`)** slices to another `[]T` - the exact same type
  as the operand. Bounds (when given) must satisfy `0 <= a <= b <= cap(s)` -
  note this is checked against **capacity**, not length: Go's own real rule
  allows a reslice to extend past the current length into spare capacity
  (the idiom Go's own slice-growth code relies on). The omitted-*high*
  default is still `len(s)`, not `cap(s)` - only an *explicit* high bound may
  reach into spare capacity.
- **A `string`** slices to another `string`, sharing the same backing bytes
  read-only (strings are immutable - see the `string` representation
  section in `CODEGEN.md`). Bounds must satisfy `0 <= a <= b <= len(s)` -
  strings have no separate capacity concept.
- **A fixed-size array (`[N]T`)** slices to a genuine **dynamic array**
  (`[]T`), not another `[N]T` - matching Go's own behavior exactly (slicing
  an array produces a slice). This requires the array operand to be
  **addressable** (an lvalue - the same rule `&`/a method receiver/an
  assignment target already follow) - a non-addressable fixed-array rvalue
  (e.g. a function call's own return value) can't be sliced, since the
  resulting slice needs a real, stable backing address to alias into. Bounds
  must satisfy `0 <= a <= b <= N` (`N` is the array's own compile-time-known
  size).

`a`/`b` are ordinary runtime `int`-typed expressions (or an untyped constant
adapting to `int`) - neither is required to be a compile-time constant,
mirroring `make`'s own `n`/`cap`. The actual `0 <= a <= b <= <bound>` range
check happens at runtime (see `CODEGEN.md`'s "Slicing" section) - a violation
traps immediately, the same hard-abort mechanism an out-of-range index or a
bad `make` size already use.

```go
s := []int{10, 20, 30, 40, 50}
mid := s[1:4]        // [20 30 40] - shares s's own backing array
print(mid)
print(len(mid))       // 3
mid[0] = 99
print(s[1])            // 99 - the write is visible through the original too

str := "hello world"
print(str[6:])         // "world" - str[a:] omits the high bound (defaults to len)
print(str[:5])         // "hello" - str[:b] omits the low bound (defaults to 0)

arr := [5]int{1, 2, 3, 4, 5}
view := arr[1:3]        // [2 3] - slicing a fixed array produces a real []int
view[0] = 100
print(arr[1])           // 100 - shares the fixed array's own storage
```

## Maps

`map[K]V` - Go-style prefix type syntax, following exactly the same
bracket-prefix, recurse-into-element-type shape `[N]T`/`[]T` already use,
just keyed on the `map` keyword instead of a leading `[`:

```go
var m map[string]int
```

A map value is a real hash table (see `CODEGEN.md`'s "Maps" section for the
exact representation/growth scheme) - **a reference type**, like Go's own
real maps: assigning one map-typed variable to another (or passing one as an
argument, or returning one) copies only a small header, not the table
itself, so both sides still observe the identical live table and its
mutations.

Deliberately scoped to **storage/lookup/removal only this round** - no
iteration (see "Explicitly out of scope" below).

- **`make(map[K]V)`** creates a fresh, empty map - unlike `make([]T, n)`,
  this takes no further arguments at all: a map always starts out empty,
  growing on demand as entries are inserted.

  ```go
  m := make(map[string]int)
  ```

- **`m[k] = v`** inserts a fresh key or updates an already-present one, in
  one uniform operation - there's no separate "insert" vs. "update" spelling,
  matching Go exactly.

  ```go
  m := make(map[string]int)
  m["a"] = 1     // insert
  m["a"] = 2     // update - m["a"] is now 2
  ```

- **`m[k]`** as a plain, single-value read yields `V` - **a missing key
  returns `V`'s own zero value**, exactly matching Go's real behavior, not a
  trap or an error:

  ```go
  m := make(map[string]int)
  m["a"] = 1
  x := m["a"]        // 1
  y := m["missing"]  // 0 - int's zero value, no error
  ```

- **`v, ok := m[k]`** (or `v, ok = m[k]` against already-declared targets) -
  Go's own "two-result index expression," reusing the multi-target
  destructuring grammar this language's multi-return values already
  introduced (see "Go-style multi-return values" below): `ok` reports
  whether the key was actually present, `v` is `V`'s zero value when it
  wasn't.

  ```go
  m := make(map[string]int)
  m["a"] = 1
  v, ok := m["a"]        // v == 1, ok == true
  v2, ok2 := m["missing"] // v2 == 0, ok2 == false
  ```

  This is a genuinely different rule from a real multi-return function call
  used in the same position (see that section's own precise wording): a map
  index expression's own type is always just plain `V` - a 2-target
  destructuring context is the only place that additionally exposes the
  `ok` component; an ordinary single-value `m[k]` (as above) is completely
  unaffected and needs no special casing at all. This mirrors Go's own real
  spec, which calls map indexing (and channel receives, which don't exist
  here) a distinct "two-result index expression" rule, never a general
  multi-value-returning expression the way a function call is.

- **`len(m)`** - the map's current live entry count, extending the same
  `len` builtin that already works on a dynamic array, a fixed-size array,
  and a `string`.

- **`remove(m, k)`** - a new, dedicated predeclared builtin that deletes `k`
  from `m` (a no-op if `k` isn't present, or if `m` is `nil` - never an
  error either way, matching Go's own `delete(m, k)`). Deliberately a
  **distinct** builtin, not a reuse of this language's existing `delete p`
  **statement** (see "Pointers" below) - that's a wholly unrelated
  operation, real pointer/heap deallocation; reusing the same keyword for
  map-key removal would be a confusing collision between two unrelated
  concepts, so this gets a clean new name instead, needing no new grammar at
  all (it's an ordinary call, exactly like `len`/`make`).

  ```go
  m := make(map[string]int)
  m["a"] = 1
  remove(m, "a")
  _, ok := m["a"]   // ok == false
  print(len(m))      // 0
  ```

**Key-type restriction.** A map key must be a type this language's own
`==`/`!=` already supports: any numeric type, `bool`, `string`, a pointer, or
a struct/fixed-size array whose own fields/elements are themselves all
comparable, recursively. A **dynamic array (`[]T`)**, a **function type**, or
**another map** as a key type is rejected outright, with a diagnostic
pointing at the key type itself, reported the moment the `map[K]V` type
itself is declared (a struct field, a `var`, a parameter, `make`'s own
argument, ...) - none of these are meaningfully hashable/comparable the way
this language currently represents them:

```go
var bad1 map[[]int]int              // error: []int is not a comparable key type
var bad2 map[func(int) int]int      // error: func(int) int is not a comparable key type
var bad3 map[map[string]int]int     // error: map[string]int is not a comparable key type

struct Point { x int; y int }
var ok map[Point]string             // fine - every field is itself comparable
```

**A map element is not addressable and not independently mutable in place.**
`&m[k]` is a compile error (mirroring Go's own identical restriction exactly
- a map slot may not even exist yet, so there's no stable address to hand
back), and compound assignment/`++`/`--` against a map element
(`m[k] += 1`, `m[k]++`) are rejected too, with a clear diagnostic - read the
current value, compute the new one, and store it back with a plain `=`
instead:

```go
m := make(map[string]int)
m["a"] = 1
m["a"] = m["a"] + 1   // fine - 2
m["a"]++               // error: map element does not support ++
```

**Nested maps and a map-typed struct field both just work** - a map's value
type is "any type," including another map, falling out for free from the
general type-position grammar with no extra work needed:

```go
struct Box {
    counts map[string]int
}

var nested map[string]map[string]int
```

**Map iteration** (`for k, v := range m`) is now supported - see "Range
loops" below. There are still no `keys(m)`/`values(m)` helper builtins.

**Explicitly out of scope this round:**

- **A map composite-literal syntax** (`map[string]int{"a": 1, "b": 2}`) -
  Go has this, but it's a real, separate grammar extension on top of this
  language's existing `CompositeLit` machinery (built around struct/array
  shapes specifically); `make(map[K]V)` plus individual `m[k] = v`
  insertions covers everything this round needs. Writing `map[...]...{...}`
  as an expression today is a plain parse error (`map` has nowhere legal to
  start an expression, since there's no literal form for it), not a panic.

## Range loops

A fourth `for` form, alongside the three plain C-style ones (see "Loops"
above): iterating a map or a fixed/dynamic array directly, rather than
driving an index by hand. Scoped narrowly and deliberately - see "Explicitly
out of scope" below - and hardcoded for performance rather than built
through any general/user-definable iterator mechanism (a later, separate
feature - see `DECISIONS.md`'s dated entry for this round).

Three binding shapes, matching Go's own real grammar exactly:

```go
for k, v := range m {     // map: k is K-typed, v is V-typed
    ...
}

for i, v := range arr {   // array (fixed or dynamic): i is int, v is the element type
    ...
}

for v := range arr {      // one binding - see the wrinkle below
    ...
}

for range m {              // zero bindings - iterate for side effects only
    ...
}
```

**The one-binding form's own wrinkle - easy to get backwards.** Go's real
rule differs by the subject's own kind, and this language follows it
exactly:

- **map, one binding**: the single name binds the **key**, not the value -
  there is no way to get "just the value" from a one-binding map range
  without a wildcard for the key.
- **array, one binding**: the single name binds the **index** (always
  `int`), not the element.

```go
m := make(map[string]int)
m["a"] = 1
for k := range m {
    print(k)   // "a" - the KEY, never 1 (the value)
}

a := [3]int{10, 20, 30}
for i := range a {
    print(i)   // 0, 1, 2 - the INDEX, never 10/20/30 (the elements)
}
```

The two-binding form is `(K, V)` for a map, `(int, elem)` for an array - the
first name is always the key/index, the second (when present) always the
value/element.

The zero-binding form (`for range subject {}`) still evaluates `subject`
exactly once (so a call expression producing the map/array still runs its
own side effects once), just declares no fresh bindings at all - useful for
counting iterations or running a side-effecting body without needing either
name.

**Explicitly out of scope:**

- **Ranging over a string** (rune iteration, Go's own `for i, r := range s`)
  - this language has no Unicode-aware string handling anywhere yet (see
    `std/strings`'s own "Deliberately deferred" note below).
- **Ranging over a bare integer** (Go 1.22's `for i := range n`) - not
  supported; a range subject must be a real map or array value.
- **Ranging over a struct, pointer, or any other type** - rejected with a
  clean diagnostic ("range requires a map or array value, got %s"), never a
  panic.
- **The `=`-reuse form** (`for k, v = range m {}`, rebinding
  already-declared variables instead of declaring fresh ones) - not
  supported; every binding is always freshly declared, the same as every
  other `for` form's own `:=`-only header.

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

**A struct field or method may be named with a reserved word** (`move` above is one - see "Destructors" below) - unlike a `var`, a free function, or a type name, a field/method is only ever reached through `receiver.name`, or declared inside a struct's own member list, never as a bare value on its own, so there's no grammar ambiguity a keyword spelling could create there. A **keyed** composite literal is the one exception: `Point{move: 1}` doesn't work, since a keyed element's own key is parsed as an ordinary expression first (mirroring Go's own `CompositeLit` shape) - and a keyword like `move` always means the start of its own construct wherever a bare value could appear (`move x`, "Destructors" below), never a plain reference to a field of that name. A **positional** literal (`Point{1}`) is unaffected either way.

## Constructors

A struct may also declare one or more `constructor(params) { body }` blocks, nested directly inside the struct declaration - a deliberate, narrow exception to "structs are data-only, methods declared separately" above:

```go
struct Point {
    x int

    constructor() {
        this.x = 99
    }
    constructor(v int) {
        this.x = v
    }
}

func main() int {
    a := Point(5)   // calls the one-arg constructor, a.x == 5
    b := Point()    // calls the zero-arg constructor, b.x == 99
    c := Point{1}   // composite literal, completely unaffected/unchanged
    return a.x + b.x + c.x
}
```

`this` inside a constructor body refers to the struct instance being constructed, exactly like inside an ordinary method - there's no separate receiver clause to write, since a constructor's receiver is always the struct it's nested inside.

**Overloaded by argument count only.** Multiple constructors on the same struct are distinguished purely by how many parameters they declare - never by parameter type. Two constructors declaring the same parameter count on one struct is a compile-time error, reported right at struct-declaration time (not call time): it's a structural problem with the struct itself, regardless of whether either constructor is ever actually called.

This is a **deliberate, narrow exception** to this language having no general function/method overloading anywhere else - it exists specifically to keep construction bound to the type itself and distinct from an ordinary function call, not as a precedent for overloading free functions or methods generally. Nothing else about "no overloading" changes: two free functions or two methods with the same name are still always a redeclaration error, regardless of their parameter lists.

**`Name(args)` calls a constructor; `Name{...}` never does.** A call-expression on a struct type name (`Point(5)`) invokes whichever constructor's declared parameter count matches `len(args)`, type-checking `args` against that constructor's parameters exactly like an ordinary function call. If no constructor's parameter count matches, or the struct declares zero constructors at all, this remains exactly as illegal as a struct-type call always was (see "Explicit conversions" above: a non-numeric conversion target is rejected) - this feature only adds a *new* legal case, it doesn't change what was already illegal. A composite literal (`Point{...}`, positional or keyed) is **completely unchanged** - it always means raw structural construction, bypassing constructors entirely, regardless of whether the struct has any constructors declared; both construction paths coexist freely on the same type.

A bare, uncalled struct type name (not followed by `(` or `{`) still means exactly what it means today - a type reference, valid only in type position - regardless of whether the struct has constructors.

**Export/visibility.** A constructor doesn't get its own independent export bit - it isn't individually named beyond the `constructor` keyword. A struct's constructors are usable cross-package if and only if the struct type itself is exported, exactly the same rule its fields and methods already follow (see "Imports" below).

## Destructors

A struct may also declare **at most one** `destructor() { body }` block, nested directly inside the struct declaration exactly like a `constructor` - the same narrow exception to "structs are data-only, methods declared separately" constructors already are:

```go
struct FileHandle {
    raw *RawHandle

    constructor(path string) {
        this.raw = new RawHandle(path)
    }
    destructor() {
        delete this.raw
    }
}
```

A destructor **always takes zero parameters** - there's no calling syntax that could ever pass any, since a destructor is never called directly by name at all (unlike a constructor, reachable via `Name(args)`): it only ever fires implicitly, at one of the two triggers below. Declaring more than one `destructor()` on the same struct is a compile-time error, reported at struct-declaration time, the same "a structural problem regardless of whether either is ever used" reasoning a duplicate-arity constructor already gets. `this` inside a destructor body resolves exactly like inside an ordinary method or constructor.

### The non-copyable rule

**A struct that declares a `destructor()` becomes non-copyable, full stop - no exceptions, including no "it happens to be the last use of this variable" leniency.** A value can only ever be handed to a new owner explicitly (`move x` - see below), never implicitly inferred from "this happens to be its last use": if a value is never duplicated, there is only ever one instance of it, so "when does it destruct" is never ambiguous.

Concretely, for any type that is non-copyable (see "Transitive propagation" below), each of the following is a real compile-time error:

- `b := a` / `b = a` where `a` is an existing value of that type - a short var decl or assignment copying an *existing, already-live* value.
- Passing such a value **by value** as a function argument - to either a free function/method or a constructor. Unlike a plain existing-value reference, this needs no exception carved out for a *fresh* value (see the next paragraph): a freshly-constructed argument is exactly as sound as a fresh var-decl initializer, since the callee's own parameter becomes that value's one and only owner, destructing it at its own scope exit, with nothing else anywhere still referencing it.
- **Returning such a value by value** from a function - see "move" below for the one exception (a fresh construction or `move x`).
- Storing an *existing* value of that type by-value into a struct field or array element - as an assignment/copy, not as fresh construction (see immediately below).

**A composite literal (`T{...}`) or a `new T(...)`/`new T{...}` call constructing a *fresh* instance is NOT a copy and remains completely legal**, even for a non-copyable type, in every context above including a return statement's value - it's creating the one instance, not duplicating an existing one:

```go
f := FileHandle(path)      // fine - constructs the one instance f now owns
g := f                     // error - copies an existing value
useFile(FileHandle(path))  // fine - the parameter becomes the sole owner
useFile(f)                 // error - f is an existing value, not fresh
```

Calling a method on a non-copyable value is completely unaffected by any of this - a method receiver is already always an implicit pointer, never a copy, so this was true before this feature and stays true.

### move

`move x` - a prefix expression, `x` always a bare identifier naming a local variable or parameter - is the other legal exception alongside fresh construction, everywhere above **including the return statement**, which used to allow no exception at all:

```go
func take(f FileHandle) { }
func make(path string) FileHandle {
    f := FileHandle(path)
    return move f          // fine - hands off f's own ownership to the caller
}
g := make("a")
take(move g)                // fine - g is never referenced again after this
```

Moving `x` transfers its value out and marks `x` itself moved-from for the rest of the current function: any later reference to `x` on any reachable path - a read, `delete x`, another `move x`, or even just letting its own scope end - is a compile-time error ("use of moved value"). A value moved on only some of two converging paths (one `if`/`else` branch, or one `match` arm, but not every other reachable one) is rejected outright as ambiguous ("may already have been moved") rather than reconciled - see DECISIONS.md's dated entry for why. The symmetric cases are both fine: every reachable branch moves it, or a branch that doesn't return/break/continue before the join. Moving a value declared outside the current loop, from inside that loop's own body, is rejected unconditionally (a later iteration could then move an already-moved value) - a value declared inside the loop body itself has no such restriction, being fresh every iteration.

A function whose own return type is non-copyable can be called as a *fresh* value at any of its own call sites (a var-decl init, an argument, nested inside another return), with no extra annotation: it could only have type-checked at all if every one of its own returns already satisfied this same fresh-or-move rule, which transitively guarantees it always hands back sole ownership.

Moving a copyable-typed value is legal and harmless - a plain read, since there's no ownership to track. `move` only ever applies to a bare identifier; moving `this.field`, an array element, or any other expression shape is a parse-time error.

### Transitive propagation

A struct containing *any* field whose own type is non-copyable is *itself* non-copyable, even if it declares no destructor of its own - the same reasoning C++ uses to implicitly delete a class's copy constructor when it has a non-copyable member. Likewise, a fixed-size array `[N]T` is non-copyable if `T` is. This is computed once per struct, checked recursively through field types.

```go
struct Wrapper {
    f FileHandle   // FileHandle has a destructor - Wrapper is non-copyable too
}
```

A **dynamic array (`[]T`) whose element type is non-copyable is rejected outright**, with a real diagnostic, rather than silently mishandled - `make`/`append`/growth all copy element bytes around (a `memcpy` on reallocation) with no destructor-cascading concept at all, so this is explicitly out of scope for this round, not a case this language quietly gets wrong.

### Firing: two triggers only

1. **A plain local variable or parameter of a type that declares its own `destructor()`** - not merely non-copyable via a field; only a type that declares its own destructor actually gets a call - fires that destructor **at every point control can leave its own declaring block**: falling off the end of the block, an early `return` inside it (or inside a block nested within it), or a `break`/`continue` that exits an enclosing loop from within it. This is exactly the same exit-shape enumeration this language's "Missing return" flow analysis already uses (see that section below) - no goto/labels/exceptions exist here, so that's the exhaustive list. At each such exit, every still-in-scope local whose type has a destructor is destructed, **in reverse declaration order**, before control actually transfers.

   A plain local of a destructor-having type needs no `new`/pointer at all to be legal - it's its own sole owner on the stack, exactly like a `new`'d pointer is on the heap, just automatically cleaned up at scope exit instead of needing an explicit `delete`:

   ```go
   func f() {
       a := Counter(10)
       b := Counter(20)
       // falls off the end here: b destructs, then a - reverse order
   }
   ```

2. **`delete p`** (see "Pointers" below, its own dedicated statement for freeing a `new`'d heap allocation) - if `p`'s pointee type declares its own `destructor()`, it's called (`p` itself as `this`) **before** the existing `free(p)` call, not after - the destructor's own body can still safely read/write through `this` one last time.

### Known limitation: no automatic recursive member destruction

Embedding a destructor-having value **by value** as a struct field does **not** automatically cascade a destructor call into that field when the containing struct's own scope ends, if the containing struct declares no `destructor()` of its own - the field's destructor simply never fires in that case. This is a real, documented v1 limitation, not a bug: the intended pattern for a resource-owning type is to hold a `*T` **pointer** field to what it owns and manually `delete` it in its own destructor body (exactly like the `FileHandle` example at the top of this section), not to embed a destructor-having value directly as a field expecting it to clean itself up automatically.

## Enums

Rust-style tagged unions: a top-level `enum Name { ... }` declaration, alongside `struct`/`func`/`extern func`/`var`, whose named variants each carry their own shape of associated data (or none at all):

```go
enum Shape {
    Point,
    Circle(f64),
    Triangle { base f64, height f64 }
}
```

Three variant kinds coexist freely in the same enum, distinguished purely by how each is written:

- **Unit variants** (`Point`) - a bare name, no associated data at all.
- **Tuple variants** (`Circle(f64)`) - positional associated data, any number of types.
- **Struct variants** (`Triangle { base f64, height f64 }`) - named associated data, reusing this language's existing `name Type` field-declaration shape verbatim (the exact same syntax a struct's own fields already use) rather than inventing a new one.

### Construction

Each variant kind has its own construction syntax, always spelled `EnumName.Variant...`:

- **Unit**: a bare, uncalled value - `Shape.Point`. This is an ordinary `MemberExpr` naming a variant with no associated data; nothing follows it.
- **Tuple**: call syntax - `Shape.Circle(5.0)` - type-checked against that variant's own declared positional types exactly like an ordinary function call's arguments.
- **Struct**: composite-literal syntax, reusing this project's existing keyed-literal grammar - `Shape.Triangle{base: 3.0, height: 4.0}`. Both keyed and positional forms work, identically to a struct's own composite literal (see "Structs" above): a keyed literal may omit fields, a positional one must supply exactly one value per field in declaration order.

```go
c := Shape.Circle(5.0)
t := Shape.Triangle{base: 3.0, height: 4.0}
p := Shape.Point
```

**Deliberately no separate `constructor(){}` block for enums** - unlike a struct (which needs constructors specifically because a bare composite literal doesn't run custom logic), variant construction already fully serves that role: there is no "raw structural construction that bypasses custom logic" to distinguish it from, since a variant *is* its own data, not a wrapper around it.

### Methods

Methods are declared exactly the same receiver-clause way a struct's own methods already are - `func (Shape) Area() f64 { ... }` - with `this` inside resolving to a pointer to the enum value, same as a struct receiver. This needed zero parser grammar changes: a receiver clause was already just an identifier token naming *some* declared type, struct or enum alike.

```go
func (Shape) Area() f64 {
    match this {
        Shape.Circle(r) => {
            return 3.14159 * r * r
        }
        Shape.Rectangle(w, h) => {
            return w * h
        }
        Shape.Point => {
            return 0.0
        }
    }
}
```

A method value (`shape.Area`, referenced without a call) remains out of scope for the same reason it is for a struct - see "First-class functions" below.

### Destructors

An enum may also declare **at most one** `destructor() { body }` block, nested directly inside the enum declaration - the exact same syntax and "at most one, checked at declaration time" rule a struct's own destructor already has (see "Destructors" above). It fires **once**, regardless of which variant is actually active, at every one of the same control-flow scope-exit points a struct's destructor already fires at (falling off the end of its declaring block, an early `return`, or a `break`/`continue` exiting an enclosing loop) - there is no per-variant destructor concept, and no way to run different cleanup logic depending on which variant happens to be live.

Declaring a destructor makes the enum **non-copyable**, exactly like a struct - the identical rule from "Destructors" above (`b := a` of an existing non-copyable value is rejected; a fresh construction or `move x` is not a copy, everywhere including a return) applies verbatim, substituting "enum" for "struct" throughout.

### Non-copyable propagation

If **any** variant's **any** associated-data type is itself non-copyable (recursively, using the same rules a struct's own field-based propagation already uses), the whole enum becomes non-copyable too - mirroring exactly how a single non-copyable struct field already taints the whole struct (see "Transitive propagation" above). A unit variant trivially contributes nothing, having no associated data to check.

```go
struct Handle {
    id int
    destructor() { }
}

enum Wrapper {
    Wrap(Handle)   // Handle has a destructor - Wrapper is non-copyable too
}
```

### Recursive and self-referential variants

A variant holding a pointer to the same enum type it's declared inside just works, falling out for free from the general rule that a variant's associated-data types are ordinary type-position types, and pointers are ordinary types:

```go
enum List {
    Cons(i32, *List),
    Nil
}
```

### Comparability and printability

An enum is comparable (`==`/`!=`) and printable (`print()`) iff **every** variant's **every** associated-data type is itself comparable/printable, recursively - not just whichever variant happens to be constructed on either side of a particular comparison/print call: this is a compile-time property that must hold across every possible runtime variant, the same way a struct's fields are all checked regardless of which code path actually sets them. The same allowlist a struct's own fields are checked against applies here (see "Maps" above and "Operators"/"The `print` builtin" below): a dynamic array, function type, or map, anywhere nested inside any variant's associated data, makes the whole enum uncomparable (a function/map also makes it unprintable; a dynamic array remains printable but not comparable, exactly like the struct case).

Two enum values compare equal iff they hold the same variant **and** that variant's own associated data compares equal, recursively (a unit variant compares equal to another of the same variant unconditionally, having no data to differ on) - genuinely a runtime property, unlike a struct's own equality (whose every field is always present): comparing/printing an enum value requires first checking which variant is actually active at runtime, then only proceeding into that variant's own data.

```go
a := Shape.Circle(2.0)
b := Shape.Circle(2.0)
c := Shape.Circle(9.0)
d := Shape.Point

a == b   // true - same variant, same data
a == c   // false - same variant, different data
a == d   // false - different variant

print(a)   // Circle(2.000000)
print(d)   // Point
```

## match

A **statement** (not an expression this round - see "Explicitly deferred" below) for dispatching on a value - either exhaustively, on an enum value's own active variant, destructuring its associated data (if any) into fresh local names scoped to the matching arm alone; or, this round's own generalization, Go-`switch`-style on a plain int/bool/string value's equality against each arm's own pattern(s). Which of the two applies is decided purely by the subject's own type - the grammar itself is identical either way:

```go
match shape {
    Shape.Circle(r) => {
        print(r)
    }
    Shape.Rectangle(w, h) => {
        print(w * h)
    }
    Shape.Point => {
        print(0)
    }
    _ => {
        print(-1)
    }
}
```

Each arm may now carry a comma-separated list of **one or more** patterns before its `=>` (Go's own `case a, b, c:` multi-value-per-arm shape) - all sharing that one arm's body:

```go
match x {
    1, 2, 3 => {
        print("low")
    }
    _ => {
        print("other")
    }
}
```

Each pattern is one of:

- `EnumName.Variant` (unit) - matches that variant, binding nothing.
- `EnumName.Variant(binding0, binding1, ...)` (tuple) - matches that variant, binding each fresh local name positionally to the variant's own declared associated-data types, scoped to that arm's body only.
- `EnumName.Variant{field0: binding0, ...}` (struct-style) - matches that variant, binding each named field to a fresh local name via an explicit `field: newLocalName` mapping, reusing the same keyed-composite-literal-style syntax construction uses.
- **an ordinary value expression** (new this round) - a literal, a variable/constant reference, or any other expression - checked for equality against the subject, exactly like a plain switch-case value in Go. Only legal when the subject itself is a value-match subject (see "Value matching" below), never alongside an enum subject.
- the wildcard `_` - matches anything not otherwise covered by an earlier arm, binding nothing. Always a lone pattern on its own arm - never combined with any other pattern in the same comma-separated list.

Each arm's body is an ordinary `Block` (braces). Control simply exits the whole `match` after one arm's body finishes running - **no fallthrough, no explicit `break` needed**, unlike C's `switch`.

### Enum matching and exhaustiveness checking

A real, hard compile-time check - this is the entire point of building `match` as its own construct rather than an unchecked switch:

- Every arm's pattern must name one of the *matched* enum's own declared variants - a pattern naming a variant belonging to some **other** enum type, or a nonexistent variant name, is a clean diagnostic, never a panic.
- No variant may be matched by more than one arm - a duplicate is a clean diagnostic.
- Either **every** variant must be covered by some arm, **or** a `_` wildcard arm must be present. Missing this is a clean "match is not exhaustive: missing variant(s) ..." diagnostic naming exactly which variants are uncovered.
- **An enum-match arm may bind only one variant pattern.** The comma-separated multi-pattern arm shape above is a value-match-only feature - `Shape.Circle(r), Shape.Point => { ... }` is a clean diagnostic ("an enum match arm may bind only one variant pattern"), not silently only checking the first pattern or attempting to unify two differently-shaped variants' bindings into one shared arm body (see `DECISIONS.md` for why this stays a deliberate scope limit rather than being bundled into this round).

```go
enum Shape { Circle(f64), Point }

match shape {
    Shape.Circle(r) => { }
}
// error: match is not exhaustive: missing variant(s) Point

match shape {
    Shape.Circle(r) => { }
    Shape.Circle(r2) => { }   // error: variant Shape.Circle already matched
    Shape.Point => { }
}
```

A fully-exhaustive `match` (every arm ending in a terminating statement, with no wildcard needed because every variant is explicitly covered - or a wildcard arm present) counts as a terminating statement in its own right for this language's "Missing return" flow analysis (see below), the same way a fully-covered `if`/`else` already does - a function whose body ends in such a `match` needs no further `return` after it.

### Value matching

`match`'s own general Go-`switch`-style extension: the subject may also be a plain `i8`/`i16`/`i32`/`i64`/`bool`/`string` value (an untyped numeric-literal subject defaults exactly like any other no-declared-type-context expression - see "Untyped numeric constants" above), rather than only an enum. Every arm's every pattern is then checked as an ordinary value expression, equality-comparable against the subject exactly like an ordinary `==` operand pair (the same untyped-literal-adapts-to-the-other-side rule that operator already follows):

```go
func classify(x int) string {
    match x {
        1, 2, 3 => {
            return "low"
        }
        4, 5 => {
            return "mid"
        }
        _ => {
            return "other"
        }
    }
}
```

**`f32`/`f64` are deliberately excluded** as a value-match subject - float equality is a footgun this language already avoids leaning into elsewhere (see `DECISIONS.md`), and it's simply rejected with a clean diagnostic, the same as any other unsupported subject type (a struct, array, pointer, map, or function value): `match requires an enum value, or an int/bool/string value to switch on, got <type>`.

**A wildcard `_` arm is MANDATORY for a value-match** - deliberately stricter than Go's own `switch`, which allows no `default` at all and simply falls through doing nothing when no case matches. A value-match's own domain (an unbounded type like `int`/`string`) has no closed set of values the way an enum's own declared variants do, so there is **no exhaustiveness check possible** here - the mandatory wildcard is what keeps `match` a real safety net regardless:

```go
func f(x int) int {
    match x {
        1 => { return 1 }
        2 => { return 2 }
    }
}
// error: value match requires a wildcard _ arm (exhaustiveness cannot be
// checked for int)
```

**Duplicate-literal detection is a nice-to-have, not a hard guarantee.** Two patterns that are both literal constants (the same literal kind, e.g. two `NumberLit`s or two `StringLit`s, with identical value) are flagged as a duplicate match case - but only when both sides are literals computable at compile time; a pattern that's a variable reference or any other computed expression is never checked against another for redundancy, the same limitation Go's own `switch` has for the identical reason (it isn't knowable until runtime):

```go
match x {
    1 => { }
    1 => { }   // error: duplicate match case 1
    _ => { }
}
```

### `match` as an expression

`match` can also be used anywhere an expression is legal - a `:=` right-hand side, a `return`, a function call argument, nested inside another expression - not just as a side-effecting statement. The two surface forms share the identical subject/pattern grammar; only the arm body's own shape differs:

```go
x := match s {
    "s" => {
        if special {
            yield "small-but-special"
        }
        yield "small"
    }
    "m", "l" => {
        yield "medium-or-large"
    }
    _ => "unknown"
}
```

Each arm picks its own body shape independently:

- **A bare expression** (`pattern => expr`, no braces) - the value *is* `expr` directly.
- **A block** (`pattern => { ... }`) - ordinary statements, `if`/`for`/whatever, with every reachable path ending in a `yield expr` statement. `yield` can appear at any nesting depth inside the block (inside an `if`, a loop, ...) - exactly like `return` can appear anywhere inside a function body.

**`yield` is a distinct keyword from `return`, deliberately** - a match-expression arm's block can contain an ordinary `return`, which still exits the *whole enclosing function* (never the match expression) exactly as it always has. Reusing `return` to also mean "produce this arm's value" would make the same keyword mean two different things depending on which lexical context it appears in - see `DECISIONS.md` for the exact ambiguity this was chosen to avoid. A missing `yield` on some reachable path through a block-bodied arm is a clean "match arm does not yield a value on every path" diagnostic, not a silent gap - mirroring this language's own "missing return" check.

**Exhaustiveness needs nothing new** - the existing enum-match (every variant covered, or a wildcard) and value-match (mandatory wildcard) rules already guarantee every match, statement or expression, covers every reachable case. A match expression where no arm's yield is ever actually reached (every arm instead ends in a `return`/`break`/`continue` of its own) is its own separate diagnostic - "match expression has no arm that ever yields a value" - since a `return`-ending arm is a legal dead end (it never needs to produce a value at all), but a whole match expression that never yields anywhere has nothing to bind its own result to.

### Explicitly deferred - not built this round

- **Binding-unification across several differently-shaped enum-variant patterns sharing one arm** (`Shape.Circle(r), Shape.Rectangle(w, h) => { ... }`) - a real, separate feature (deciding what a shared arm body may even reference when each pattern binds different names/types), deliberately not bundled into this round's multi-pattern-arm grammar, which stays enum-match-arm-restricted to exactly one pattern (see "Enum matching" above).

## Pointers

A real pointer type `*T` - one level of indirection to any other type
(a primitive, a struct, an array, or another pointer - `**T` is legal). Two
operators produce/consume one:

- **`&x`** (address-of) - `x` must be *addressable*: a plain variable/
  parameter, a struct field (`&p.x`), an array element (`&a[0]`), or another
  pointer's own dereference (`&*p`). It is **not** legal on a bare value that
  has no real storage of its own - a literal (`&5`), a function name (`&f` -
  see "First-class functions" above: `f` alone already means something
  different there), or any other expression that isn't one of the shapes
  above.
- **`*p`** (dereference) - `p` must itself be a pointer; `*p` reads (or, as
  an assignment target, writes) the value it points to. `*p = v` is a legal
  lvalue, exactly like `.field`/`[index]` (see "Assignment" below).

```go
x := 5
p := &x     // p is *int
*p = 10     // x is now 10
y := *p     // y is 10
```

**`new T(args)` / `new T{...}`** heap-allocates a `T` and returns a `*T` -
the one way to obtain a pointer that outlives the current function's own
stack frame. Both existing construction forms work unchanged, just wrapped:
a struct's own constructor call (`new Point(1, 2)`, requires at least one
declared `constructor` - see "Constructors" above) or a composite literal
(`new Point{1, 2}`, `new [3]int{1, 2, 3}`) - `new` itself only decides
*where* the value is built (a real heap allocation instead of a stack slot
or inline field), not *how*. Wrapping anything else (`new someFunc()`,
`new i64(5)`) is rejected - `new` is not a general-purpose allocator syntax,
only a heap-allocating spelling of construction that already exists.

```go
struct Point {
    x int
    y int

    constructor(px int, py int) {
        this.x = px
        this.y = py
    }
}

p := new Point(1, 2)     // *Point, via the constructor
q := new Point{3, 4}     // *Point, via a composite literal
```

**`delete p`** frees a heap allocation obtained from `new`, calling straight
through to libc's real `free` - a genuinely separate heap from the bump-
allocator arena string concatenation/dynamic arrays use (see `CODEGEN.md`),
so `delete`ing a `new`'d pointer is a real, individually-freeable
deallocation, not a no-op against a never-freed arena. `p` must be a real
pointer type; `delete` itself produces no value, matching `break`/`continue`
being their own dedicated statement forms rather than call-shaped builtins.
If `p`'s pointee type declares its own `destructor()` (see "Destructors"
above), it's called - `p` itself as `this` - before the free, not after; a
pointee type with no destructor behaves exactly as before this feature, a
plain `free` and nothing else.

There is essentially no automatic memory management here - with one narrow,
deliberate exception: when `delete`'s own operand is a bare local variable or
parameter (`delete p`, not `delete p.next`/`delete arr[i]`/a copy held in a
second variable), that variable's own storage slot is additionally
overwritten with a null pointer immediately after the free. This turns the
single most immediate mistake - reusing the *same variable* right after
deleting it - into a deterministic, clean null-pointer-dereference trap
instead of silent memory corruption:

```go
p := new Point(1, 2)
delete p
*p = Point{0, 0}   // p is nil here - traps cleanly, does not corrupt
                    // whatever memory got reallocated into the freed block
```

This mitigation is real but intentionally narrow - it covers exactly one
case and no others. All of the following remain completely unmitigated, real
use-after-free/double-free bugs, exactly as unchecked as they would be in C:

- a pointer reached through a struct field (`delete p.next; *p.next = ...`)
  or an array/slice element (`delete arr[i]; *arr[i] = ...`) - only a bare
  variable's own slot is ever nulled, never a field/element's storage;
- a second variable or parameter holding a copy of the same now-freed
  address (`q := p; delete p; *q = ...`) - each variable has its own
  independent slot, so nulling `p`'s slot has no effect on `q`'s;
- deleting a value that's still reachable through another pointer at all,
  in general - `delete` has no notion of aliasing or ownership, so nothing
  here ever prevents a *different* still-live pointer to the same freed
  block from being dereferenced.

```go
p := new Point(1, 2)
delete p
```

**Auto-deref for member access.** `p.field`/`p.method(...)` on a `*T` works
exactly like `(*p).field`/`(*p).method(...)` - matching Go's own automatic
pointer-dereference rule for selector expressions, so a heap-allocated value
reads and calls just like a plain struct value would. This is scoped to
member access only - **indexing does not auto-deref**: `(*p)[0]`, not
`p[0]`, for a `*[N]T`.

```go
p := new Point(1, 2)
print(p.x)        // 1 - reads exactly like (*p).x
p.x = 10          // writes exactly like (*p).x = 10

func (Point) move(dx int, dy int) {
    this.x = this.x + dx
}
p.move(5, 0)      // the receiver is the same heap Point - mutates in place
```

**`nil`** is a predeclared value naming a null pointer - deliberately scoped
to pointer types only, not a general zero-value/`interface{}` concept this
language doesn't have. Like a numeric literal, it starts out *untyped* and
only becomes a concrete `*T` once a pointer-typed context pins one down (a
declared `*T` variable, or the other side of an `==`/`!=` comparison); unlike
an untyped numeric constant, **it has no default type to fall back to** - a
context that never provides one (`p := nil`, `print(nil)`) is a compile
error, not a silent default.

```go
var p *Point = nil   // fine - nil adapts to *Point
if p == nil {
    p = new Point(1, 2)
}

q := nil             // error: cannot use nil without a pointer type context
```

`*T` is an ordinary type everywhere else a type can appear - a `var`
declaration, a function parameter/return type, a struct field, an array
element type. Two pointer types are equal (for `==`/`!=`, or any assignment/
argument check) iff their pointee types are - `*Point` and `*int` never mix,
same "no implicit conversion" rule every other type in this language follows
(see "Types" below).

## Assignment

`=` assigns to any lvalue: an identifier, `.field`, `[index]`, or a
dereference `*p`. Compound assignment and increment/decrement are supported:
`+=  -=  *=  /=  ++  --` (none of these apply to a pointer itself - there's
no pointer arithmetic in this language).

```go
a = 5
p.x = 10
arr[0] = 1
*ptr = 20
x += 1
x++
```

## Functions

`return` supports either a single value (as always) or, now, several -
Go-style multi-return values, this language's answer to error handling
(there is still no exception/panic-recover mechanism anywhere - see
"Missing return" below and `DECISIONS.md`'s dated entry confirming this
direction). A function's return-type position may declare a parenthesized,
comma-separated list of 2 or more types instead of a single type (or no
return type at all, still completely unchanged):

```go
func divide(a int, b int) (int, bool) {
    if b == 0 {
        return 0, false
    }
    return a / b, true
}
```

A `return` statement inside such a function must supply exactly that many
values, in order, each individually assignable to its own matching component
type (the same per-position assignability check an ordinary single-value
`return`/argument/assignment already uses):

```go
func f() (int, bool) {
    return 1, true   // fine
    return 1          // error: function returns (int, bool); return must
                       // supply 2 values
    return 1, 2, 3     // error: wrong number of return values: got 3, want 2
    return true, 1     // error: cannot use bool as int in return value 1
}
```

### Destructuring only - no first-class tuple type

**There is no tuple type anywhere in this language.** A multi-return call's
result can only ever be consumed by immediately destructuring it, right at
the one call expression that produced it - it can never be stored in a
single variable, passed around as one value, or used any other way. This is
a deliberate restriction, not a gap - it mirrors Go's own actual rule
exactly (Go itself has no tuple type either; a multi-value call result is
just as unstorable there).

Two new statement forms exist purely to destructure one - a multi-name short
variable declaration (all names freshly declared):

```go
result, ok := divide(10, 2)   // result == 5, ok == true
```

and a multi-target assignment (every target already an existing, ordinary
lvalue - a plain variable, a struct field, or an array/slice element,
exactly the same shapes a single-target `=` already allows):

```go
var result int
var ok bool
result, ok = divide(10, 0)    // result == 0, ok == false

p.field, arr[0] = divide(20, 4)   // targets don't have to be plain idents
```

If a component type is itself non-copyable (see "Destructors" above), destructuring the call is a fresh construction for that component, not a copy - the same "callee's own return already proved fresh-or-move" reasoning the single-value rule uses, applied per component.

In both forms, **the right-hand side must be exactly one call expression**
(or, for a 2-target destructuring, a map index - see "Maps" above) whose
callee's own signature returns exactly as many values as there are targets
on the left - no mixing with other expressions, no partial application:

```go
a, b := f(), g()      // error: right-hand side of a short variable
                       // declaration must be exactly one function call
a, b, c := f()         // error (if f returns 2 values): wrong number of
                        // values in short variable declaration: call
                        // returns 2, got 3 target(s)
```

Using a multi-return call's result anywhere else - assigned to a single
name, passed as an argument, printed directly, or any other single-value
position - is a compile error, not a panic or a silently-truncated value:

```go
func f() (int, bool) { return 1, true }

x := f()        // error: multi-value result (int, bool) cannot be used as
                 // a single value; it can only be destructured immediately
                 // (a, b := ... / a, b = ...) or returned matching a
                 // function's own multi-return type
print(f())       // error, same reason
```

### General Go-style parallel multi-assignment

`a, b := 1, 2` and `a, b = 1, 2` also work - each side individually evaluated
and paired positionally, nothing to do with a multi-return call at all. This
reuses the identical `MultiShortVarDecl`/`MultiAssignStmt` grammar the
call-destructuring forms above already use - the sole difference is a genuine
comma-separated value list on the right (wrapped in the same `MultiValueExpr`
node `return a, b, ...` already uses for its own value list) instead of a
single call/map-index expression:

```go
a, b := 1, 2          // a == 1, b == 2
x, s := 5, "hi"        // each position independently typed: x is int, s is string
```

Every value is type-checked (and, where untyped, defaulted) completely
independently, position by position - never unified against each other or
against some other position's type the way a `BinaryExpr`'s own two operands
would be. A count mismatch either way is a clean diagnostic, matching Go's own
real wording:

```go
a, b := 1, 2, 3   // error: assignment mismatch: 2 variables but 3 values
a, b, c := 1, 2   // error: assignment mismatch: 3 variables but 2 values
```

**Evaluation order: every value first, then every target, exactly like Go.**
Every value on the right is evaluated, in source order, before any target on
the left is written to - this is what makes the classic swap idiom actually
swap, rather than silently reading a just-overwritten value back out:

```go
a := 1
b := 2
a, b = b, a   // a == 2, b == 1 - a genuine swap, not a=b followed by
              // b=a's own already-clobbered a
```

A single-target `a := 1, 2` (no second name/target on the left at all) still
gets a real, clean diagnostic rather than a confusing raw syntax error:

```go
a := 1, 2   // error: assignment mismatch: 1 variable but 2 values
```

See `examples/multi_assign/multi_assign.llx` for the full worked example
(parallel init, the swap idiom, and mixed-type positions), and
`DECISIONS.md`'s dated entry for why this reuses `MultiValueExpr` rather than
a new node kind.

**Still deliberately out of scope** - each a real, separate feature in its
own right, not needed for the motivating error-handling use case (or, for
argument-spreading, this round's own parallel-multi-assignment use case
either):

- **Argument-spreading** (Go's own `f(g())`, forwarding a multi-return
  call's results onward as multiple arguments to another call). Every
  argument position is an ordinary single-value context, so a multi-return
  call there is rejected exactly like any other single-value position.
- **No first-class tuple type.** `MultiValueExpr` stays a syntax-only wrapper
  legal only in the destructuring position above (and `return`'s own
  position) - it is never itself a real, storable value usable anywhere else,
  the same restriction the call-destructuring case above already has.
- **A blank identifier (`_`) for discarding one of several destructured
  values.** This language has no blank-identifier concept anywhere yet (see
  `src/sema/resolve_test.go`'s own note on this) - every destructured value
  must bind to (or assign into) a real, distinctly-named target. Likely
  worth revisiting once/if a blank identifier is ever added generally, but a
  deliberate, documented gap for now, not an oversight.

`main` needs no special-casing for any of this: it already only accepts "no
return type or exactly `int`" (see below), so a multi-return-typed `main` is
simply rejected by that exact same existing check, unchanged.

### The `main` function's return type

The function literally named `main` (no receiver) is special: it may declare
no return type at all, or exactly `int` - any other declared return type
(`func main() f64 { ... }`, `func main() string { ... }`, ...) is a compile
error ("main must return either nothing or int"), enforced by
`sema.checkFuncDecl`'s `checkMainReturnType` (`src/sema/typecheck.go`). This
is a real, user-visible language rule, not just an internal implementation
detail: `main` must ultimately hand a real process exit code back to the
OS (a plain `i32` - see `CODEGEN.md`'s "`main` is the real entry point"
section for how a declared-or-omitted `int` return type actually lowers),
so no other return type could ever be a meaningful value there.

```go
func main() {
    // fine - no declared return type, falls off the end with exit code 0
}

func main() int {
    return 0 // fine
}

func main() f64 {
    return 1.5 // error: main must return either nothing or int, got f64
}
```

## Types

Primitive types: signed integers `i8`, `i16`, `i32`, `i64`; unsigned integers
`u8`, `u16`, `u32`, `u64`; floats `f32`, `f64`; `string`; `bool`. `int` is not
a separate type - it's exactly a synonym for `i32` (see `sema.TypeInt`'s doc
comment: both spellings produce the literal same `Type` value, so
`var a int = 1` and `var b i32 = a` need no conversion between them at all -
see `DECISIONS.md` for why this width was chosen). The unsigned types have no
analogous single-width alias: `int` is special only as the language's oldest
int type. Each `uN` is its own distinct type, identical to the same-width
`iN` in every way except signedness - the explicit-only-conversion and
no-implicit-mixing rules below apply identically, so an `i32` and a `u32` no
more interoperate without a conversion than an `i32` and an `i64` do. A
negative constant (e.g. `-1`) never adapts to an unsigned type; negating an
unsigned *variable* wraps (two's-complement), matching Go. A named struct type, an array type (`[N]T` fixed-size, `[]T`
dynamic), and a pointer type (`*T` - see "Pointers" above) round out the type
system - see their own sections above.

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

Scoped to **numeric-to-numeric only**, plus two dedicated FFI crossings -
`cstring(s)` and `string(cs)` (see "The `cstring` type" above) - checked as
their own special case ahead of the numeric-only rule. Every other
conversion whose target or argument isn't numeric (`i64("hello")`,
`Point(x)`, `bool(x)`) is rejected: `struct`/`array`/`bool` conversions
aren't meaningfully "conversions" in the C-cast sense this feature covers,
and are out of scope. A wrong argument count (`i64(1, 2)` or `i64()`) is also
a real error
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
  matching width/kind, `string`, `bool`, the exact same struct/array type, or
  the exact same pointer type - `Type.Equal`, `sema/types.go`) and produce
  `bool`. Two structs are equal iff every corresponding field is equal,
  recursively (a field can itself be a struct or array); two arrays are equal
  iff every corresponding element is equal, recursively - the same rule Go
  itself uses. That recursion also requires every field/element to itself be
  a comparable type, exactly the same rule the "Maps" section's
  key-type restriction already states (a **dynamic array (`[]T`)**, a
  **function type**, or **another map**, anywhere nested inside the struct/
  array, is rejected with a compile-time diagnostic, not just at the top
  level) - a struct or array containing one of those is simply not
  comparable at all, on either side of `==`/`!=`. Two pointers compare equal
  iff they hold the exact same address (identity, not pointee-value comparison
  - dereference first,
  `*a == *b`, to compare what they point to). `nil` (see "Pointers" above)
  is a special case on either side of `==`/`!=` against a pointer - it's
  never itself a pointer *value*, only ever an untyped placeholder that
  adapts to whichever concrete `*T` the other operand is; comparing `nil`
  against `nil` directly is rejected (there's no pointer type either side
  could adapt to). Comparing two *different* struct/array/pointer types (or
  a struct against an array, or a non-pointer against `nil`) remains a
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
exactly one argument, of any *printable* type, and returns nothing (`void`).
"Printable" is any numeric type, `bool`, `string`, a pointer, or a
struct/array whose own fields/elements are themselves all printable,
recursively (`sema.typeIsPrintable`) - a **function type** or a **map**,
anywhere nested inside a struct/array, is rejected with a compile-time
diagnostic instead of ever reaching codegen. This is a strictly larger set
than what `==`/`!=` accepts (see the "Operators" section above): a dynamic
array (`[]T`) prints fine but is never comparable. See `CODEGEN.md`'s
"`print` builtin, concretely" section for how it renders each type at
runtime.

## The `args()` builtin

`args()` is predeclared exactly like `print`/`make`/`append`/`len` (see
`sema.universeScope`) - a real, no-argument call, callable from anywhere in a
program (not just `main`), returning the program's own command-line
arguments as a `[]string`:

```go
func main() int {
    a := args()
    print(len(a))
    i := 0
    for i < len(a) {
        print(a[i])
        i++
    }
    return 0
}
```

Takes **no arguments at all** - `args(1)` is a compile error ("args takes no
arguments, got 1"), unlike `make`/`append`/`len`, which all take at least
one. Always returns `[]string` - an ordinary dynamic array, so `len(...)`,
indexing, slicing, and `for` all work on its result exactly like any other
`[]string` value, with no special casing anywhere past the call itself.

**Constructed once, at program startup** - not re-marshaled on every call.
Every call to `args()` anywhere in a program observes the identical value,
computed a single time before the program's own logic ever begins running.

**Real command-line arguments, when the program is a real, standalone,
AOT-compiled executable** (`llvmc -o program.exe program.llx` - see
`CODEGEN.md`'s "`-o`: AOT compilation to a native executable" section):
`args()[0]` is the running executable's own path, and `args()[1:]` are
whatever arguments it was actually invoked with, exactly like Go's
`os.Args`/C's `argv`:

```powershell
.\program.exe foo "bar baz"
# args() == [".\program.exe", "foo", "bar baz"]
```

**A real, honest, and deliberately narrower fallback under JIT execution**
(`llvmc program.llx`, no `-o`): `args()` always returns an **empty**
`[]string` (`len(args()) == 0`) - `llvmc` does not capture or forward any
trailing command-line arguments typed after the compiled program's own path
this round (a `foo`/`bar` written after the path is a plain usage error, not
something forwarded into the running program). See `CODEGEN.md`'s own
"`args()` builtin" section and `DECISIONS.md`'s dated entry for exactly why
this fallback was chosen over threading real argv through the JIT's own
raw-syscall invocation mechanism - the practical tradeoff was judged not
worth the real regression risk (or genuine implementation awkwardness) the
alternative would have introduced. This is the one, narrowly-scoped way
`args()`'s behavior actually depends on *how* the same program is run, not
just what it does - documented here precisely so it's never a surprise.

## Missing return

A function declaring a return type must be guaranteed to return a value on
every possible execution path - `sema.Check` runs a full flow analysis for
this (`isTerminatingStmt`, `src/sema/typecheck.go`), modeled directly on Go's
own spec ("Terminating statements"), cut down to this language's smaller
statement grammar (no goto/labels/switch/select/panic exist here - `match` is
the one exception, see below). A statement list ends in a terminating
statement if its last statement does, where a terminating statement is one
of:

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
- a `match` (see "match" above) that is both exhaustive (every one of the
  matched enum's own variants covered by an arm, or a wildcard `_` arm
  present) and whose every arm's own body is itself terminating - mirroring
  an `if`/`else`'s identical "every branch present and terminating" rule,
  generalized from two branches to N. A `match` missing either property (an
  arm that falls through, or a variant left uncovered with no wildcard) is
  never terminating, the same "there's always a path that falls straight
  through" reasoning an incomplete `if`/`else` already has.
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

See `CODEGEN.md`'s "First-class functions"/"Lambdas" sections for how a
function value actually lowers to LLVM IR (the fat-pointer representation,
and the direct-vs-indirect call distinction) - that's an implementation
concern, not a language-spec one, so it lives there instead of here.

A struct field of function type is an **ordinary field** - calling through
it (`cb.fn(5)`) works exactly like calling through a func-typed variable or
parameter, no different from any other indirect call:

```go
struct Callback {
    fn func(int) int
}

func double(x int) int {
    return x * 2
}

func main() int {
    cb := Callback{double}
    return cb.fn(5)   // 10 - calls through the field directly
}
```

This is deliberately unlike a **method value** (`p.move`, referenced without
a call), which remains out of scope and still a compile error (see above) -
`cb.fn` is an ordinary field holding a function value, not a bound method
closing over some receiver, so the same restriction never applied to it in
the first place.

## Lambdas (function-literal expressions)

A function value doesn't have to be a *reference* to an already-declared
free function (see "First-class functions" above) - `func(params)
[returnType] { body }` is also a real, value-producing *expression* in its
own right, usable anywhere any other expression is legal: an argument, a
`var`'s initializer, a `return` value, even called immediately. This is a
genuinely new grammar rule (`FuncLit`), not just a new use for the existing
`func(T1, T2) R` *type* syntax - today, a `func` keyword followed by a body
only ever appeared at top level (`FuncDecl`) or as a method declaration;
`func(params) { ... }` used as an expression didn't parse before this round
at all.

```go
func makeCounter() func() int {
    count := 0
    increment := func() int {
        count = count + 1
        return count
    }
    return increment
}

func main() int {
    next := makeCounter()
    print(next())   // 1
    print(next())   // 2
    return next()   // 3
}
```

**Capture is by reference, matching Go's own closures exactly** - not by
value. A lambda that reads or writes a variable/parameter declared in an
*enclosing* function (`count` above, closed over by `increment`) shares that
variable's real storage, not a snapshot taken at the moment the lambda was
created: `count`'s mutations are visible both inside and outside the lambda,
and multiple lambdas capturing the same variable observe each other's
writes, exactly like the example above (`count` keeps incrementing across
three separate calls to the one `increment` value `makeCounter` returned,
because it closes over `count`'s real storage). This crosses arbitrarily many
enclosing function levels, not just one - a lambda nested inside another
lambda can still capture a variable declared in the outermost function, the
same way Go allows.

An immediately-invoked lambda works too, since a `FuncLit` is an ordinary
expression and calling it is just the ordinary call grammar applied to
whatever expression comes before `(`:

```go
result := (func() int {
    return 42
})()
```

**What's captured, exactly**: any variable or parameter a lambda's body
reads or writes that isn't declared inside the lambda itself (or a lambda
nested inside it) - including a variable declared in a lambda *two or more*
function levels up. Referencing an enclosing method's `this` from inside a
lambda is explicitly rejected ("cannot capture `this` inside a function
literal") - method-receiver capture isn't supported this round, the same
"scoped narrower than it might eventually be" precedent "First-class
functions" above already set for bound method values.

A lambda's own exposed *type* is indistinguishable from a plain function
reference's - both are simply `func(paramTypes) returnType` (see
"First-class functions" above) - the capture mechanism is purely a
representation/lowering concern, invisible at the type-checking level. See
`CODEGEN.md`'s "Lambdas" section for the fat-pointer representation this
reuses verbatim (the same `{fnPtr, ctxPtr}` shape "First-class functions"
above already introduced, forward-compatible with exactly this feature from
the start - `ctxPtr` finally does real work here, instead of always being
null), the heap-promotion of a captured variable's storage, and the
uniform-calling-convention fix a genuine closure's `ctxPtr` needed.

**A `for` loop's own header variable gets fresh per-iteration semantics when
captured (Go 1.22+ style), not the older, shared-slot behavior every other
captured variable still gets.** This is the one deliberate exception to
"capture is by reference, sharing one real storage location" above -
specifically for a variable declared in a `for` loop's own init clause
(`for i := 0; ...; i++ { ... }`), when a lambda created inside the loop's
body captures it directly:

```go
fns := make([]func() int, 0)
for i := 0; i < 5; i++ {
    fns = append(fns, func() int { return i })
}
// fns[0](), fns[1](), ..., fns[4]() -> 0, 1, 2, 3, 4
// (NOT 5, 5, 5, 5, 5 - the classic pre-1.22-Go closures-in-a-loop gotcha)
```

Each closure sees the value `i` held at its *own* iteration, not whatever
`i++` has mutated it to by the time the closure is actually called - exactly
matching modern Go's own loop-variable semantics (see `DECISIONS.md` for why
this project deliberately diverges from its own prior implicit behavior to
match it). Mutations the loop body itself makes to the header variable still
propagate forward correctly into the next iteration's condition/post-clause
check - `continue` doesn't lose them - so ordinary uses of the loop variable
(as a counter, an index, mutated mid-body) behave exactly as before; only a
lambda's own captured view of it gets a private per-iteration copy.

This is narrower than it might sound - it applies *only* to the loop's own
init-declared name, never to a variable declared anywhere else and merely
read or reassigned inside a loop body:

```go
total := 0
for i := 0; i < 5; i++ {
    total = total + i   // ordinary shared-storage read/write, unaffected
}
```

and it's silently skipped (falling back to today's exact shared-slot
behavior) for a **non-copyable** loop variable - a struct declaring its own
`destructor()`, or a fixed-size array of one, recursively (see "Destructors"
below) - since giving it fresh per-iteration semantics would mean implicitly
copying it once per iteration, which this language's "non-copyable, zero
exceptions" rule never allows anywhere else either. In practice this is a
non-issue today: nothing currently lets a non-copyable type be a `for` loop's
own header variable in the first place, this is purely a defensive
guarantee should the grammar ever grow one.

## Generator functions

A `yield T` return-type marker on a top-level `func` declares a **generator
function** - C#-style, producing a sequence of `T` values one at a time
rather than a single return value:

```go
func Range(a int, b int) yield int {
    for i := a; i < b; i++ {
        yield i
    }
}
```

Inside a generator's own body, `yield expr` (the same `yield` keyword
"match" above already introduced for match expressions - see that section's
own "`yield` is a distinct keyword from `return`" note) produces one value
of the sequence; `expr` must be assignable to the declared element type `T`.
An ordinary `return` **with a value** is illegal inside a generator's own
body - a generator "returns" only by finishing its body or exiting early
with nothing left to produce - but a **bare** `return` (no value) is legal
and means "stop yielding, exit now":

```go
func FirstNPositive(n int) yield int {
    count := 0
    i := 1
    for {
        if count == n {
            return   // stop yielding early - fine
        }
        yield i
        count = count + 1
        i = i + 1
    }
}
```

A generator function's own body needs no "missing return" check - falling
off the end is legitimate (it simply means "no more values"), exactly like
an ordinary function declaring no return type at all.

### Consuming a generator

A generator call (`Range(1, 10)`) is consumed via the same `for ... range
...` grammar "Range loops" above already introduced - a generator subject
supports only the **zero-binding** and **one-binding** forms, never
two-binding: there is no key/index concept for a generator the way a map has
a key or an array has an index, only the single yielded value.

```go
for v := range Range(1, 10) {   // one binding - v is the yielded value
    if v == 5 {
        break
    }
    print(v)
}

for range Range(0, 5) {         // zero binding - side effects only
    ...
}
```

`break`/`continue` inside the consuming loop work exactly like any other
`for` form - `break` stops consuming the generator early, `continue` skips
straight to the next yielded value:

```go
sum := 0
for v := range Range(1, 6) {
    if v == 3 {
        continue   // skip 3, keep going
    }
    sum = sum + v
}
// sum == 1+2+4+5 == 12
```

A generator call's own result has no real standalone runtime representation
under this lowering (see `CODEGEN.md`'s "Generator functions" section for
why) - it is legal **only** directly as a range-for's own subject
expression, called directly by name - a package-qualified name
(`mathutils.Range(1, 10)`) works exactly the same way, since it's just as
direct a call as a same-package one. Every other use is a clean
compile-time diagnostic, never a panic:

```go
x := Range(1, 10)        // error - a generator's result can't be stored
print(Range(1, 10))      // error - can't be passed as an argument either
for k, v := range Range(1, 10) { }   // error - produces at most 1 value, not 2

g := Range
for v := range g(1, 10) { }          // error - must call the generator
                                      // directly by name, not through a
                                      // stored function value
```

A generator function can't be a method (`yield T` on a receiver-declared
`func` is rejected) - only a plain top-level function.

### Explicitly out of scope

- **Multi-value yields** (`yield (k, v)` producing a pair). This language's
  `yield T` syntax names exactly one type, so a generator supports only the
  one-binding or zero-binding consuming forms, never two-binding.
- **Nested generator composition** - a generator function's own body ranging
  over *another* generator, forwarding its yields onward. This needs the
  inner synthesized callback to capture the outer generator's own implicit
  yield-callback parameter, which this language's existing capture analysis
  (built around named identifier references, not an invisible codegen-only
  parameter) doesn't obviously cover - a real, separate feature for later,
  rejected with a clean diagnostic today rather than silently mis-compiled.
- **True suspend/resume** - calling a generator's `.Next()` externally,
  pausing mid-function, holding two live generators concurrently and
  stepping them independently. See `CODEGEN.md`'s "Generator functions"
  section and `DECISIONS.md` for why a push/callback lowering was chosen
  over this instead - and see this file's own "Coroutines" section below,
  a genuinely separate feature built on real LLVM coroutine intrinsics
  rather than push/callback, for exactly this capability.

## Coroutines

Real suspend/resume coroutines - `async func`/`await`, a caller-held handle
driven by hand via `resume`/`done`/`delete` - distinct from, and not built on
top of, this language's own `yield T` generator functions above (see
`DECISIONS.md`'s dated entry for why these are two separate features rather
than one generalized over the other).

```
async func Sequence() {
    print(1)
    await
    print(2)
    await
    print(3)
}

func main() {
    h := Sequence()          // runs eagerly up to the first await - prints 1
    for !done(h) {
        resume(h)             // runs to the next await (or completion)
    }
    delete h                  // safe no-op here: already done, just frees the frame
}
```

- **`async func Name(params) { body }`** - a new top-level declaration form,
  modeled on a plain `func` (same params, receiver, and body grammar) with
  one addition: `await` is legal anywhere inside its own body, at any
  nesting depth (exactly like `return` is legal anywhere inside an ordinary
  function). Never a method (a receiver clause combined with `async` is a
  clean diagnostic) and never a `FuncLit` - `async` is a top-level-only
  marker this round, with no closures/captures for a coroutine's own frame
  at all.
- **`await`** - a bare statement, no operand, no result. Suspends the
  enclosing coroutine at exactly that point; the next `resume(h)` against
  its own handle continues execution immediately after it.
- **Calling an async function returns a coroutine handle** (`h := Sequence()`)
  - unlike a generator call (only ever legal as a `range`-for's own subject),
  a coroutine handle is a real, storable value: assign it, hold it, pass it
  around. It's non-copyable, exactly like a destructor-owning struct - only
  a fresh call result may be assigned to a new name; `h2 := h` (aliasing an
  existing handle) is a clean diagnostic.
- **`resume(h) bool`** - runs the coroutine once, from wherever it's
  currently suspended, until its next `await` or until it finishes. Returns
  whether there's more work left (`true`) or it just finished (`false`).
  Calling it on an already-finished handle is a safe, defined no-op
  returning `false` - never undefined behavior.
- **`done(h) bool`** - reports whether the coroutine has already finished
  (normally, or via `delete`/scope exit). Safe to call at any time.
- **`delete h`** - reuses this language's existing `new`/`delete` vocabulary
  (see the "Pointers" section) to explicitly destroy a not-yet-finished
  coroutine early: every local still live at its current suspend point gets
  destructed (in reverse declaration order, exactly like an ordinary scope
  exit), then its frame is freed. A coroutine handle falling out of scope
  *without* an explicit `delete` gets the identical automatic cleanup - the
  same non-copyable, destructor-owning-value machinery a struct with its own
  `destructor()` already gets (see "Destructors"). Calling `delete` twice on
  the same handle, or `resume`/`done` after it, is a safe, defined no-op -
  never undefined behavior.
- **`coroutine`** - a predeclared type keyword naming `TypeCoroutine`
  directly, usable anywhere an ordinary type name is legal (a `var`
  declaration, a struct field, a function parameter) - the same non-copyable
  rules above apply identically regardless of spelling: only a fresh async
  call may fill a `coroutine`-typed slot, never an existing handle. A struct
  containing a `coroutine` field is non-copyable like any other destructor-
  owning struct (see "Destructors"), so a dynamic array of such a struct is
  rejected the same way; store it behind a pointer instead (see
  `std/scheduler`'s own `Entry` for the idiom).

### Explicitly out of scope this round

Deliberately the smallest useful core primitive - "one handle, driven by
hand, no scheduler, no timers" - matching how `range` shipped before
generator functions built on top of it:

- **No return value.** An async function declares no return type at all -
  `async func f() int { ... }` is a clean diagnostic. Reading a coroutine's
  own final result (once `done(h)` is true) needs `llvm.coro.promise`-based
  storage this round doesn't build - see `CODEGEN.md`'s "Coroutines" section
  for the full reasoning behind deferring this rather than half-building it.
- **No built-in timers/scheduler.** The core primitive itself is still a
  bare `await`, resumed purely by an explicit `resume(h)` call - `std/scheduler`
  now provides a Unity-`StartCoroutine`-style `Schedule`/`Tick` API on top of
  it, entirely as ordinary stdlib code (see "Standard library" below), not a
  compiler feature.
- **No coroutine-to-coroutine interleaving.** An async function can't itself
  call and await another async function - only hand-written driver code
  (like `main` above) resumes a handle. This is arguably the most natural
  next round after a scheduler, but is out of scope here.
- **No closures.** `async func` is top-level-only, exactly like an
  `ExternFuncDecl` - never a `FuncLit`, so there's no capture-analysis work
  at all this round (a coroutine's own frame - which locals are live across
  which suspend point - is entirely `CoroSplit`'s problem, not the
  frontend's).

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

**A `scheme:path` import is the one exception** - a path with no `./`/`../`
prefix, but with a colon before its first `/` (`std:mathutil`, not
`./std/mathutil`), resolves against that scheme's own root instead,
completely independent of the importing file's location:

```go
import "std:mathutil"

func main() f64 {
    return mathutil.Sqrt(16.0)
}
```

- **`std:`** reaches this compiler's own bundled standard library - see
  "Standard library" below. `std:collections/slotmap` (a nested package)
  works the same way as `std:mathutil` (a flat one): everything after the
  colon is just an ordinary path within that root.
- **`lib:`** is reserved for third-party packages, but isn't implemented
  yet - importing under it is a clear compile error for now, not a silent
  no-op or a fallback to some other resolution.

A colon appearing anywhere *after* the path's first `/` (or not at all) is
never treated as a scheme - `foo/bar:baz` is an ordinary relative path. A
colon *before* the first `/` is illegal in a Windows path outright, and this
project currently only targets Windows/mingw64 (see `DECISIONS.md` - no
second platform to worry about yet), so `std:mathutil` could never
legitimately be a real relative path on this project's one supported
platform - there's no ambiguity with a project's own local package happening
to be named `std` or `lib`.

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

## `tests{}`

A `tests { ... }` block lets test code live in the same file as the code it
tests, legal anywhere a top-level declaration is (same position as
`import`/`struct`/`func`/`var`/`enum`/`extern func`), in any file:

```go
func add(a int, b int) int {
    return a + b
}

tests {
    import "std:test"

    func TestAdd(t *test.Runner) {
        t.AssertEqual(add(1, 1), 2, "1+1")
    }
}
```

Its body may hold any top-level declaration - `import`, `struct`, `enum`,
`func`, `var`, or `extern func` - under the same rules as an ordinary file
body (imports must come first).

**Its contents are only visible to a `-test` build.** Run with `llvmc -test`
(see `compiler.md`'s "Run language tests" section) and everything inside a
`tests{}` block behaves like ordinary top-level code: its own `import`
resolves normally, and `TestXxx` functions are discovered exactly like a
standalone test file's (that convention - a separate test package/file - is
unaffected and still fully supported; `tests{}` is additive, not a
replacement). Compiled any other way (a plain run, `-emit-llvm`, `-o`,
`-watch`), a `tests{}` block and everything inside it is completely inert:
no compile cost, no exported symbols, and its own imports (`std:test`
included) never need to resolve at all.

**Nesting a `tests{}` block inside another is a compile error.**

See `DECISIONS.md` for why this is a parse-time decision rather than a mode
flag threaded through later compiler stages.

## Generics

Functions and structs may declare type parameters in square brackets, Go-style:

```go
func Sum[T](a T, b T) T {
	return a + b
}

struct SlotMap[T] {
	items       []T
	generations []i32
	freeList    []int
}
```

**No constraints, checked per instantiation.** This language has no
interfaces, and generics add none: a type parameter is unconstrained, and a
generic body is type-checked separately for each concrete instantiation the
program actually reaches, against that instantiation's real types. So
`Sum(1, 2)` and `Sum("a", "b")` both compile, `Sum(p, p)` for a struct `P`
with no `+` is an ordinary "operator + not defined for P and P" diagnostic
reported at that call's own instantiation, and a generic nobody ever
instantiates is never checked at all. See `DECISIONS.md` for why this model
was chosen over Go's, and `CODEGEN.md` for how it lowers.

**Type arguments are inferred at call sites**, by matching each declared
parameter's type against the corresponding argument's already-known type -
through slices, pointers, maps, function types, and other generic structs
alike:

```go
func Head[T](items []T) T { return items[0] }

s := make([]int, 1)
print(Head(s))   // T = int
```

A type parameter used inconsistently across two parameter positions is an
inference failure, not a silent pick: `func Pair[T](a T, b T)` called as
`Pair(1, "x")` is an error. Untyped numeric constants contribute their
default type (`Sum(1, 2)` infers `int`, `Sum(1.5, 2.5)` infers `f64`).

**Explicit instantiation** is the escape hatch for anything inference can't
solve - most commonly a type parameter that appears only in the return type,
or a generic struct constructed with no arguments to infer from:

```go
func NewSlotMap[T]() SlotMap[T] {
	return SlotMap[T]{make([]T, 0), make([]i32, 0), make([]int, 0)}
}

ints := NewSlotMap[int]()      // required - T is unreachable from the arguments
b := SlotMap[Entity]{...}      // a generic struct's own construction/type syntax
var m SlotMap[int]             // and its type-position spelling
```

`Foo[T]` is deliberately the same syntax as array indexing; which one a given
`Foo[T]` is depends purely on whether `Foo` names a generic declaration, and
is decided once, during type-checking.

**Multiple type parameters** are allowed and inferred independently:
`func Pair[A, B](a A, b B) A`, `struct Pair[A, B] { ... }`,
`Pair[int, string](1, "a")`.

**Methods.** A struct's methods implicitly share its type parameters - the
receiver clause names them, and may spell them differently than the struct's
own declaration does (they're positional):

```go
func (SlotMap[T]) Insert(v T) int { ... }
func (SlotMap[E]) Get(i int) E    { ... }   // same parameter, different name
```

A method may **also** declare type parameters of its own, independent of its
receiver's - including on a completely non-generic struct:

```go
func (Entity) Describe[T](tag T) T { ... }  // T is inferred per call
func (Box[T]) Map[U](f U) T       { ... }   // T from the receiver, U per call
```

A method's own type parameter may not reuse a name its receiver clause
already binds (`func (Box[T]) Get[T](...)`) - that's an error, not shadowing:
the body would have no way to name the outer one, and the two are genuinely
different types.

**Not supported this round:** generic enums, explicit type arguments on a
*method* call (`p.m[int](x)` has no spelling - method type parameters are
inference-only), and using a generic name as a value without instantiating it
(`f := Id`). A generic that instantiates itself at an ever-larger type
(`F[T]` calling `F[Box[T]]`) has no finite set of instantiations and is
rejected once its type arguments nest past a fixed depth. Ordinary breadth -
however many distinct instantiations a program legitimately reaches - is not
capped in any way a real program can hit.

## External functions (FFI)

`extern func Name(params) RetType` declares a function this compiler doesn't
generate a body for at all - a binding to a real external C symbol, resolved
at link/JIT-execution time rather than lowered by this package's own codegen.
It lives at top level, alongside `import`/`var`/`func`/`struct`:

```go
extern func abs(x i32) i32

func main() int {
    return abs(-5)   // 5 - a real call into libc's own abs
}
```

A call to an extern-backed function looks, type-checks, and codegens exactly
like a call to an ordinary `func` - same argument-count/type checking, same
direct-call lowering, same ability to appear anywhere a call expression can
(a bare statement, ignoring the result; an operand inside a larger
expression; assigned to a variable). There is no separate "declare, then
bind" step and no special call syntax - the only difference from an ordinary
`func` is that this one has no `{ body }` at all, ever:

```go
extern func QueryPerformanceCounter(counter *i64) bool

func elapsed() i64 {
    start := i64(0)
    QueryPerformanceCounter(&start)   // result ignored, as a bare statement
    // ...
    end := i64(0)
    ok := QueryPerformanceCounter(&end)   // result used, as an ordinary bool
    return end - start
}
```

**The declared name is the linked symbol name, verbatim** - there is no
separate alias/rename clause this round. `extern func abs(...)` binds exactly
the symbol named `abs`, wherever the linker/JIT finds it exported from.

**No receiver, no body, no `"C"`-style ABI string.** An extern func can never
be a method (there's no grammar for a receiver clause here) and is always
terminated right after its optional return type, exactly like a type-less
`var` already is for statement termination - there's nothing after that to
parse, ever. There's also no ABI-string annotation the way some other
languages spell this (`extern "C"`): this project only ever targets one ABI
(the C calling convention, via mingw64/libc) - there is nothing to
disambiguate, so nothing to write.

**Type restriction.** Every parameter type and the return type of an extern
func must be **FFI-safe**: one of

- a numeric type (`i8`/`i16`/`i32`/`i64`/`f32`/`f64`)
- `bool`
- `cstring` (a raw `char*` - see "The `cstring` type" below)
- a pointer type (`*T`, recursively - `T` itself is never restricted, since a
  pointer is always just a raw address at the ABI level regardless of what it
  points to)
- a named struct type, **iff every one of its fields is itself FFI-safe,
  recursively** (a nested struct is fine as long as its own fields are; see
  below)

`string`, a dynamic array (`[]T`), and a function type are all explicitly
rejected with a compile error, not silently mishandled, both as a bare
parameter/return type and as a struct field: none of these have a
well-defined "just pass this to a real C function" representation in this
compiler's current ABI-level shape (each is really a small fat struct/
closure under the hood, not a single scalar/pointer value a C caller would
recognize) - solving that is explicitly out of scope for this round. A
**fixed-size array (`[N]T`) is FFI-safe only as a struct field**, never as a
bare parameter/return type by itself - a real C array parameter decays to a
pointer, a conversion this compiler doesn't perform implicitly, so there's
no legal way to pass one directly:

```go
struct Point { x int, y int }             // every field FFI-safe -> Point itself is

extern func bad1(s string) int            // error: string is not supported
extern func bad2() []int                  // error: []int is not supported
extern func bad3(cb func(int) int) int    // error: a function type is not supported
extern func bad4(a [4]int) int            // error: a bare fixed-size array is not supported

extern func ok1(p *string) int   // fine - a pointer is always just an address,
                                  // whatever it points to
extern func ok2(s cstring) i64   // fine - a raw C string is just a pointer too
extern func ok3(p Point) Point   // fine - Point is FFI-safe by value
```

A struct containing even one non-FFI-safe field (a `string`, a `[]T`, a
function type, or another struct that isn't itself FFI-safe) is rejected the
same way that field's own bare type would be:

```go
struct Bad { s string }
extern func bad5(b Bad) int   // error: Bad has a non-FFI-safe field

struct Buf { data [4]i8 }     // a fixed-size array field is fine, unlike bare
extern func ok4(b Buf) int    // fine
```

### The `cstring` type

`cstring` is a predeclared builtin type, like `string`/`bool` - not a
keyword. Unlike `string`'s own `{ptr, i32}` fat struct, `cstring` is a raw
pointer with no length, matching C's own `char*` exactly - the reason it may
cross an extern func signature while `string` may not. There is no `cstring`
literal syntax and no operator support (`+`, `==`, `print`, `len`, indexing -
none are defined for it); the only way to produce or consume one is the pair
of explicit conversions below.

```go
extern func strlen(s cstring) i64

s := "hello"
c := cstring(s)     // arena-copies s's bytes plus a trailing NUL
n := strlen(c)      // a real C call, n == 5

back := string(c)   // strlen's own length, then an arena copy of the bytes
```

`cstring(s)` (`s` a `string`) produces a NUL-terminated buffer: a string
literal argument is already NUL-terminated internally and is passed through
directly, but any other `string` value (a variable, a concatenation result,
...) is arena-copied to a fresh `len(s)+1`-byte buffer with the trailing NUL
appended, since a language string's own representation carries no such
guarantee. If `s` itself contains an embedded NUL byte, C APIs that read the
result via `strlen` (or equivalent) see a truncated string at that byte -
matching Go's `C.CString`, not a crash. `string(cs)` (`cs` a `cstring`) does
the reverse: `strlen(cs)` finds the real length, then those bytes are
arena-copied into a fresh `string` - a copy, not a borrow, so the result
stays valid independent of whatever produced `cs`. Neither conversion is
implicit - `cstring`/`string` never adapt to each other anywhere except
through `T(x)`.

### `cfunc`: bare C function pointers

`cfunc(T1, T2) R` (an optional return type, exactly like `func`) is a
**bare C function pointer type** - a keyword, like `func`/`map`, and its own
distinct type, never just a variant of `func`. Unlike an ordinary `func`
value (a fat `{fnPtr, ctxPtr}` closure pointer - see "First-class
functions" below), a `cfunc` value is a single, bare function pointer with
no capture context at all, matching a real C function pointer's own ABI
exactly - the type this language uses for **passing one of its own
functions to C as a callback**:

```go
extern func apply_callback(cb cfunc(int) int, x int) int

func double(x int) int {
    return x * 2
}

func main() int {
    return apply_callback(double, 21)   // 42 - a real C call through cb
}
```

`cfunc` is FFI-safe (it may appear anywhere the "Type restriction" section
above allows an FFI-safe type - a parameter/return, or recursively as a
struct field) - every one of its own parameter/return types must themselves
be FFI-safe, the identical recursive rule an extern func's own signature
already follows. An ordinary `func` type is still rejected on an extern
signature exactly as before - `cfunc` doesn't loosen that rule, it adds a
second, narrower type for the one shape a real C ABI can actually call.

**Only a direct reference to a top-level `func`/`extern func` may become a
`cfunc` value** - assigned to a `cfunc`-typed variable/parameter/field, or
passed as a `cfunc`-typed argument, its signature must structurally match:

```go
func add(x int, y int) int { return x + y }

var cb cfunc(int, int) int = add   // fine - add's own real address
```

A function literal, or any function value already stored in a variable/
parameter/field (even one that's itself `func`-typed and never actually
captures anything), is rejected - there is no trampoline this round to
synthesize a real, context-free C function pointer for a closure:

```go
var bad cfunc(int) int = func(x int) int { return x }   // error: no closures
f := add
var bad2 cfunc(int, int) int = f                        // error: not a direct reference
```

A struct-by-value parameter/return additionally requires the source to be
an `extern func`, not an ordinary `func` - see `CODEGEN.md` for why an
ordinary `func`'s own real signature can't safely stand in for a `cfunc`
value there. Calling a `cfunc` value is a direct call with no leading
context argument at all, using the identical Windows x64 ABI coercion an
extern func call already applies to a struct-by-value parameter/return
(see `CODEGEN.md`'s "External functions (FFI)" section).

**Explicitly out of scope for this round** (deliberately deferred, not
built): `extern var` (binding an external global variable), variadic extern
functions, rename syntax for the linked symbol name, and any platform other
than Windows. Struct-by-value FFI marshaling and `cfunc` **are** built (see
above) - this entry previously deferred both. See `DECISIONS.md` for why
this round is scoped this narrowly, and `CODEGEN.md` for how an extern func
declaration (struct-by-value ABI coercion included) actually lowers.

**A caller obligation this restriction doesn't cover: binding a real Win32
`BOOL`-returning API as this language's `bool`.** This language's own `bool`
lowers to a single LLVM `i1` (see `CODEGEN.md`) - exactly one bit. A real
Win32 `BOOL` (as `QueryPerformanceCounter`/`QueryPerformanceFrequency` above
both are, and as `std/time` binds them) is actually a 32-bit `int` whose own
ABI-documented contract is only "nonzero means TRUE", never "exactly bit 0
is set" - a genuine, if rare in practice, real-world `BOOL` implementation
is free to return any nonzero value at all. Declaring such a function's
return type as this language's `bool` truncates that real 32-bit value down
to its own single low bit, so a real result like `2` or `256` would silently
read back as `false` here. This isn't a bug in this compiler today - every
actual Win32 API this project currently binds this way happens to only ever
return exactly `0` or `1` - but it's a real caller obligation, not something
`extern func`'s own type-checking catches: before binding an external
function whose true return convention is "any nonzero value means true"
rather than "exactly 0 or 1" as this language's `bool`, verify the real
implementation's actual observed return values first, or bind its return
type as `i32` instead and compare against zero explicitly at the call site.

## Standard library

A "standard library" in this project is nothing more than ordinary `.llx`
packages living under a `std/` directory - reached via the `std:` import
scheme (see "Imports" above), not a relative path, so it works identically
regardless of where the importing project or the compiler itself are
installed:

```go
import "std:mathutil"
import "std:strings"
import "std:time"
import "std:slices"
import "std:collections"
```

At compile time, `std:` resolves against a `std/` directory expected to sit
right next to the running `llvmc`/`llvmc-lsp` executable - this repo's own
`std/` is a plain sibling of `examples/` at the repo root, exactly where
`build.ps1` puts `llvmc.exe`/`llvmc-lsp.exe`, so this repo's own examples
work the same way any other installed copy of the compiler would. A missing
`std/` sibling isn't a hard failure on its own (a program that never imports
`std:...` doesn't need one) - only an actual `std:` import surfaces a clear
error if it can't be found.

**What's available so far:**

- **`std/mathutil`** - thin wrappers around five libc `<math.h>` functions
  (`Sqrt`, `Pow`, `Floor`, `Ceil`, `Fabs`, all `f64`-in/`f64`-out, bound via
  `extern func` - see "External functions (FFI)" above), plus three generic
  (see "Generics" above) pure-`.llx` helpers needing no libc call at all:
  `Abs[T]`, `Min[T]`, `Max[T]` (comparison-based; `Min`/`Max` are exercised
  at both `int` and `f64` in this project's own examples, `Abs` at `int`
  only so far). Named "mathutil", not "mathutils", to read distinctly from
  `examples/imports`'s own unrelated same-named demo fixture.
- **`std/strings`** - `Contains`, `IndexOf` (mirroring Go's own
  `strings.Index`, `-1` when not found), `HasPrefix`, `HasSuffix`,
  `TrimSpace` (ASCII space, `0x20`, only - not full Unicode whitespace),
  `Split` (Go-`strings.Split` semantics exactly, edge cases included:
  `Split("", "")` is an empty slice, `Split(s, "")` splits into single-byte
  pieces, a `sep` that never occurs returns a length-1 slice holding `s`
  itself unchanged), `ToUpper`/`ToLower` (ASCII `a`-`z`/`A`-`Z` only), and two
  number-formatting helpers: `IntToString` (handles `0` and the smallest
  representable `int` correctly) and `F64ToString` (a fixed 4-decimal-place
  format, not a general shortest-round-tripping float formatter). Every one
  of these is hand-written using only this language's own existing
  primitives (slicing, `len`, `==`, `+`, loops) - deliberately zero `extern
  func` anywhere in this package, since `string`'s own `{ptr, i32}`
  representation has no real C-ABI shape `extern func`'s type restriction
  would even accept (see "External functions (FFI)" above).
- **`std/time`** - `Now() i64` (a raw performance-counter tick count) and
  `ElapsedSeconds(startTicks i64) f64`, a nicer API on top of the exact same
  `QueryPerformanceCounter`/`QueryPerformanceFrequency` externs
  `examples/scope_timer.llx` already binds directly - that example is left
  untouched; this package is an additive convenience layer, not a
  replacement for it. The tick frequency is cached once via a non-constant
  top-level `var` initializer (see "Global `var` initializers" above).
- **`std/scheduler`** - a Unity-`StartCoroutine`-style timer scheduler built
  on top of the `coroutine` type (see "Coroutines" above): `Scheduler.Schedule(e *Entry)`
  (honors whatever `e`'s coroutine already wrote into `e.NextWait`),
  `Scheduler.ScheduleDelayed(e *Entry, initialDelay f64)` (overrides just the
  first resume's timing - see `std/scheduler/scheduler.llx`'s own doc comment for why
  this is split out rather than one function), `Scheduler.Tick(dt f64)`,
  `Scheduler.HasPending() bool`. `Entry` owns one
  coroutine handle plus its own resume timing, always held behind a pointer
  (`[]*Entry`, never by value - see "Destructors"' dynamic-array rule).
  `Entry.Handle`/`Entry.NextWait` are exported so the calling package can
  construct one directly (`e.Handle = SomeAsyncFunc()`) - `Schedule` itself
  only ever takes an already-built `*Entry`, never a bare `coroutine`, since
  this language's non-copyable rule has no move semantics to hand one off
  through an ordinary parameter (see `DECISIONS.md`'s dated entry).
- **`std/slices`** - generic algorithms over dynamic arrays (see "Generics"
  above): `Contains[T]`, `IndexOf[T]` (`-1` when absent), `Reverse[T]`
  (in place), and three that also take a first-class function value (see
  "First-class functions"/"Lambdas" above) - `Map[T, U]`, `Filter[T]`,
  `Reduce[T, U]` (left fold). Each is checked per instantiation, same as any
  other generic: `Contains` only compiles for a `T` some call site actually
  instantiates with a comparable type.
- **`std/collections`** - `SlotMap[T]`, a generational-handle container:
  `Insert(v T) Handle`, `Get(h Handle) T`, `Remove(h Handle)`,
  `Valid(h Handle) bool` (see the package's own doc comment for the
  generational-handle mechanism itself). Promoted from the same shape
  `examples/generics/generics.llx` first proved out; that example is left
  untouched, this is an additive package on top of it (same reasoning
  `std/time`'s own entry above gives for `examples/scope_timer`).
- **`std/test`** - soft-fail test helpers for `llvmc -test`: `Runner`,
  `NewRunner`, `Assert` / `AssertFalse` / `AssertEqual[T]` /
  `AssertNotEqual[T]` / `AssertNil[T]` / `AssertNotNil[T]` /
  `AssertSliceEqual[T]` / `AssertApprox`. Discovery looks for
  `func TestXxx(t *test.Runner)` in the entry package (see CODEGEN.md's
  `-test` section). No auto-formatting of values into messages.

**Deliberately deferred, not built this round:** Unicode-aware string
handling (everything above is ASCII-only, matching this language having no
Unicode awareness anywhere else yet), a general/shortest-round-tripping
float-to-string formatter (`F64ToString` above is a simple fixed-precision
one instead), file I/O, and anything else not listed above - `std/` is
expected to keep growing incrementally, the same way any other part of this
language does.

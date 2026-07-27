# Advanced features

## Generators

A generator pushes values into a `for ... range` loop:

```go
func Range(start int, end int) yield int {
    for i := start; i < end; i++ {
        yield i
    }
}

for value := range Range(0, 3) {
    print(value)
}
```

Generators are consumed directly by a range loop. Their result cannot be
stored, passed, or called through a function variable. A consuming loop may
bind the yielded value or bind nothing; `break` and `continue` work normally.

A generator may use a bare `return` to stop early. It cannot return a value,
be a method, yield several values at once, or range over another generator
from inside its own body.

## Coroutines

Calling an async function starts it immediately. It runs until its first
`await` or completion, then returns a handle:

```go
async func Sequence() {
    print(1)
    await
    print(2)
}

h := Sequence()
for !done(h) {
    more := resume(h)
}
delete h
```

`resume(h)` runs to the next `await` or completion and returns whether more
work remains. `done(h)` checks completion without advancing. Both are safe
after completion.

Coroutine handles are non-copyable. `delete h` destroys a suspended
coroutine early; otherwise it cleans itself up at scope exit. Repeated
`delete`, `resume`, or `done` calls after cleanup are safe no-ops.

`coroutine` is the handle's type and may be used for parameters and struct
fields. A struct containing one is also non-copyable.

An async function may declare a real return type, checked like an ordinary
function's:

```go
async func ComputeAnswer() int {
    await
    return 42
}

h := ComputeAnswer()
resume(h)
answer := result(h)
delete h
```

`result(h)` reads `h`'s own declared result once it's `done`; called before
that, or after `h` has been `delete`d, it returns the result type's zero
value instead. `result` on a coroutine declaring no return type is a
compile error.

Async functions still cannot be methods, lambdas, closures, or directly
await other coroutines - calling, resuming, or reading another coroutine's
own result from inside one works today.

Coroutines require the normal optimization pipeline. Do not compile them
with `-no-opt`.

For timed work, use `std/scheduler`.

## Any

`Any` is a built-in, type-erased boxed value. Box a value with the same
call syntax an explicit conversion uses:

```go
a := Any(5)
b := Any(somePoint)
```

Every scalar type (`i8`...`i64`, `u8`...`u64`, `f32`, `f64`, `bool`,
`string`, `cstring`, a pointer), any struct, any array (fixed or dynamic),
any map, and any enum can be boxed - a struct/array only if every one of its
own field/element types is, and an enum only if every one of its own
variants' every associated-data type is, all recursively (`Any` itself can't
be nested this way - see [Any](LANGUAGE.md#any) in the language spec). A map
is boxable regardless of its own key/value types, but a boxed map's own
entries aren't reflectable - `AnyFields` sees zero fields, and `AnyAs[T]`
only checks that `T` is a map, not which key/value types. Boxing copies the
value into a fresh allocation, so a boxed value stays valid even after the
code that boxed it returns.
Collecting into a `...Any` variadic parameter boxes each argument
automatically, no `Any(x)` needed - see
[Variadic parameters](functions-and-generics.md#variadic-parameters).

Six builtins read a boxed value back out:

```go
AnyKind(a)              // a raw kind ordinal (i32)
AnyName(a)              // a display name, e.g. "int" or "Point"
v, ok := AnyAs[int](a)  // the real value if the kind matches, else (0, false)

for name, value := range AnyFields(a) {
    fv, ok := AnyAs[int](value)
}

n := AnyLen(a)             // a boxed array's own length
e, ok := AnyIndex(a, 0)    // a boxed array's i'th element, bounds-checked
```

`AnyAs[T]` always needs an explicit type argument - there is nothing left in
a boxed value to infer it from. `AnyFields` walks a boxed struct's own
fields, or a boxed enum's own *active variant's* associated data (nothing for
a unit variant, positional names for a tuple variant, real field names for a
struct variant), each itself boxed as an `Any`. `AnyName` on a boxed enum is
its active variant's own name (`"Circle"`, not the enum's own type name),
mirroring `print()`'s own convention. `AnyLen`/`AnyIndex` are permissive
about the wrong kind: calling either on a non-array `Any` (or an
out-of-range index) just returns `0`/`(zero Any, false)`, not an error.

Not boxable this round: function values and any non-copyable type. `Any`
cannot be compared with `==`, printed with `print`, or cross an
`extern func` signature. See [Current limitations](current-limitations.md)
and [the Any section](../LANGUAGE.md#any) of the language spec for the
exact rules.

## Type registry

Every declared struct/enum and every primitive type has a stable id, usable
from ordinary code:

```go
TypeId[Point]()         // Point's own id, an explicit type argument
TypeIdOf(somePoint)     // the same id, from a value's own static type

ids := TypeByName("Point")  // every registered type's id named "Point"

a, ok := AnyNew(id)         // a fresh, zero-valued Any of id's own type
ok2 := AnySet[int](field, 99) // write into a field obtained via AnyFields
```

An enum's id is always the enum type's own id, never a specific variant's.
`AnyNew` rejects an out-of-range id, an enum's own id, a non-copyable
struct/array's own id, and `Any`'s own id (`ok = false`, never a crash). `AnySet[T]` is
`AnyAs[T]`'s write-side mirror - it's what makes a struct field from
`AnyFields` actually mutable. See
[Type registry](../LANGUAGE.md#type-registry) in the language spec for the
exact rules.

## Calling C

Declare a C symbol with `extern func`:

```go
extern func abs(value i32) i32

func main() int {
    return abs(-5)
}
```

Extern signatures may use numeric values, `bool`, pointers, `cstring`,
`cfunc`, and compatible structs. Language strings and dynamic arrays cannot
cross the C boundary directly.

Convert strings explicitly:

```go
extern func strlen(value cstring) i64

size := strlen(cstring("hello"))
```

`cfunc` is a bare C callback pointer. Only direct references to top-level
functions can be used as callbacks; closures cannot.

FFI support currently targets Windows and the C ABI used by mingw64. Extra
libraries can be supplied with `-L` and `-l`; see [Compiler commands](compiler.md).

`bool` is one bit in llvm_lang. C APIs using a 32-bit “any non-zero is true”
type should be declared as `i32` unless they are known to return only `0` or
`1`.

Try:

- [`generators.llx`](../examples/generators/generators.llx)
- [`coroutines.llx`](../examples/coroutines/coroutines.llx)
- [`scheduler_demo.llx`](../examples/scheduler_demo/scheduler_demo.llx)
- [`scope_timer.llx`](../examples/scope_timer/scope_timer.llx)
- [`any_demo.llx`](../examples/any_demo/any_demo.llx)

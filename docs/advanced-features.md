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

Async functions currently have no result value and cannot be methods,
lambdas, closures, or directly await other coroutines.

Coroutines require the normal optimization pipeline. Do not compile them
with `-no-opt`.

For timed work, use `std/scheduler`.

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

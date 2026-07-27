# C interop

Declare a C symbol with `extern func`:

```go
extern func GetTickCount64() u64

func main() {
    print(GetTickCount64())
}
```

The declaration name is the linked symbol name. The current ABI target is
64-bit Windows through mingw64.

Safe signatures use numeric types, `bool`, `cstring`, pointers, `cfunc`, and
FFI-safe structs. Language `string`, dynamic arrays, maps, ordinary function
values, `Any`, and non-copyable types cannot cross the boundary.

## C strings

`cstring` is a borrowed NUL-terminated C pointer:

```go
var name cstring = cstring("Ada")
var copy string = string(name)
```

Converting `string` to `cstring` creates NUL-terminated storage. Converting
back copies bytes up to the first NUL. A `cstring` has no indexing, slicing,
or `len`.

A `cstring` can be compared against `nil`, and a `*u8`/`*i8` value converts to
`cstring` directly (a pointer reinterpret, not a copy) - together these let
you null-check a nullable C binding before converting it to a `string`:

```go
extern func getenv(name cstring) *u8

p := getenv(cstring("PATH"))
if p == nil {
    // not set
} else {
    s := string(cstring(p))
}
```

There is no conversion back from `cstring` to `*u8`/`*i8`.

## Callbacks

Use `cfunc` for a C function pointer:

```go
extern func Register(callback cfunc(i32) i32)

func Double(value i32) i32 {
    return value * 2
}

Register(Double)
```

A callback must be a direct top-level language function or `extern func`.
Lambdas and stored function values cannot become `cfunc`.

Pass library search paths and names with [`llvmc -L` and `-l`](compiler.md#link-libraries).
External variables, variadic C functions, symbol aliases, and non-Windows
ABIs are not supported yet.

Try [`scope_timer.llx`](../examples/scope_timer/scope_timer.llx).

[Previous: Any and reflection](any-and-reflection.md) ·
[Next: Compiler and editor tools](compiler.md)

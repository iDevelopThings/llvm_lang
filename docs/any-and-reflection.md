# `Any` and reflection

`Any` stores a boxed copy of a value:

```go
value := Any(42)
print(AnyName(value)) // int

number, ok := AnyAs[int](value)
if ok {
    print(number)
}
```

`AnyAs[T]` always needs an explicit type argument. A failed conversion
returns `T`'s zero value and `false`.

Boxing supports numbers, booleans, strings, pointers, structs, enums, fixed
and dynamic arrays, and maps. Functions, C callbacks, and non-copyable values
cannot be boxed. Boxing an existing `Any` is a cheap no-op copy.

## Inspect a value

The reflection helpers are:

| Helper | Result |
| --- | --- |
| `AnyKind(value)` | Raw `i32` kind number |
| `AnyName(value)` | Type, field, or variant name |
| `AnyFields(value)` | Generator of named fields |
| `AnyLen(value)` | Fixed or dynamic array length |
| `AnyIndex(value, index)` | `(Any, bool)` array lookup |

`AnyFields` exposes struct fields and the active payload of a directly boxed
enum:

```go
for name, field := range AnyFields(value) {
    print(name)
}
```

Map entries are not exposed. `AnyLen` returns `0` for a non-array, and
`AnyIndex` returns `(zero Any, false)` for a non-array or invalid index.

## Type registry

Each usable type has a stable `int` identifier:

```go
id := TypeId[Point]()
same := TypeIdOf(Point{1, 2})
```

`TypeIdOf` inspects its expression's type without evaluating the expression.

Look up declared structs or enums by name:

```go
for _, id := range TypeByName("Point") {
    value, created := AnyNew(id)
    if created {
        for name, field := range AnyFields(value) {
            if name == "x" {
                AnySet[int](field, 10)
            }
        }
    }
}
```

`AnyNew` creates a zero-valued boxed scalar, pointer, struct, array, or map.
It rejects invalid IDs, enums, `Any`, and non-copyable structs or arrays.
`AnySet` changes storage represented by a field `Any` when its type matches.

`Any` itself is neither printable nor comparable and cannot cross an FFI
boundary. See [current limitations](current-limitations.md#any-and-reflection)
for the less common reflection gaps.

Try [`any_demo.llx`](../examples/any_demo/any_demo.llx) and
[`type_registry_demo.llx`](../examples/type_registry_demo/type_registry_demo.llx).

[Previous: Coroutines](coroutines.md) ·
[Next: C interop](ffi.md)

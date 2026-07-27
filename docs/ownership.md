# Ownership and `move`

Most values copy normally. A struct or enum becomes non-copyable when it has
a destructor:

```go
struct File {
    handle i64

    destructor() {
        closeHandle(this.handle)
    }
}
```

Its destructor runs when the owning local leaves scope, including through
`return`, `break`, or `continue`. Locals are destroyed in reverse declaration
order.

Destructors are not recursively invented for fields. A struct or enum only
receives this cleanup when it declares its own `destructor()`.

## Transfer with `move`

Use `move` when ownership should change:

```go
first := File{openHandle()}
second := move first
```

After the move, `first` is invalid. Reading it, moving it again, or taking its
address is an error.

`move` accepts a bare local variable. It cannot move a field, index, pointer
target, or global.

You can move into:

- a new local;
- an assignment target;
- a function argument;
- a return value;
- a struct or fixed-array literal.

The destination must have one clear owner. Moving only on one side of an
`if`, or inside a loop when later iterations could reuse the value, is
rejected.

## Propagation

Fixed arrays and structs containing a non-copyable value are also
non-copyable. Move the whole outer value:

```go
owned := [1]File{File{openHandle()}}
transferred := move owned
```

Dynamic arrays, maps, `Any`, variadic storage, and coroutine arguments cannot
contain non-copyable values.

Heap values created with [`new`](pointers.md#heap-allocation) are released
with `delete`; `delete` calls the pointee destructor first.

[Previous: match](match.md) ·
[Next: Pointers](pointers.md)

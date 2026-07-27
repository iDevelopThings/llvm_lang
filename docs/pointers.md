# Pointers

`&value` takes an address and `*pointer` reads or writes through it:

```go
value := 10
pointer := &value
*pointer = 20
print(value)
```

The operand of `&` must be stable storage: a local, global, field, array
element, slice element, or dereferenced pointer. Map elements and temporary
values have no address.

Member access automatically follows a pointer:

```go
value := Point{x: 1, y: 2}
point := &value
point.x = 3
```

Indexing does not auto-dereference a pointer to an array; use `(*items)[0]`.

## Heap allocation

`new` allocates a constructed value:

```go
point := new Point{x: 3, y: 4}
print(point.x)
delete point
```

`new` accepts a struct constructor call, struct literal, or fixed-array
literal. It does not accept an arbitrary scalar expression.

`delete` accepts a pointer local. After deletion, a bare local is set to
`nil`; aliases are not. Deleting `nil` is safe.

## `nil`

`nil` needs a pointer type from its context:

```go
var next *Node = nil

if next == nil {
    print("end")
}
```

Pointers compare by address. Pointer arithmetic is not supported.

Pointers are intentionally low-level: returning the address of a local,
using an alias after `delete`, or keeping a pointer after its storage expires
is not prevented automatically.

[Previous: Ownership and move](ownership.md) ·
[Next: Function values and lambdas](function-values.md)

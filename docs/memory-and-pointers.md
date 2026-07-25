# Ownership, move, and pointers

You can ignore this page until you need explicit ownership or C interop.

## Copyable and non-copyable values

Most values copy normally. A struct or enum becomes non-copyable when it
declares a destructor or contains non-copyable data. Fixed arrays inherit
this rule from their element type.

Fresh construction is still legal:

```go
resource := Resource(1)
consume(Resource(2))
```

Dynamic arrays of non-copyable elements are rejected because growing the
array would need to copy them.

## Move semantics

`move name` transfers a non-copyable local or parameter to a new owner:

```go
func create() Resource {
    resource := Resource(1)
    return move resource
}

resource := create()
consume(move resource)
```

After a move, the old name cannot be read, moved, deleted, or allowed to
reach its normal scope exit.

`move` accepts only a bare local name—not a field, index, or general
expression. A move must be unambiguous on every control-flow path. Moving an
outer value from inside a loop is rejected because a later iteration could
move it again.

## Pointers

`&` takes an address and `*` reads or writes through it:

```go
x := 10
p := &x
*p = 20
print(x)
```

`nil` needs a pointer type from its context:

```go
var p *int = nil
```

Member access automatically dereferences pointers to structs:

```go
point := new Point{2, 3}
print(point.x)
```

Indexing does not auto-dereference.

## Heap allocation

`new` allocates a constructed value. `delete` runs its destructor, when
present, and frees the allocation:

```go
point := new Point{2, 3}
delete point
```

There is no general protection against aliases, double frees, or use after
free. Treat `new` and `delete` with the same care as `malloc` and `free`.

After `delete p`, a bare local `p` is set to `nil`. Copies of that pointer,
fields, and indexed pointers are not changed.

## Destructors

A struct or enum may declare one destructor:

```go
struct Resource {
    id int

    destructor() {
        print(this.id)
    }
}
```

Plain local values are destroyed automatically when their scope exits, in
reverse declaration order. A pointer created by `new` is destroyed when
passed to `delete`.

A type with a destructor is non-copyable. Transfer a local value with
`move`:

```go
func consume(value Resource) {
}

resource := Resource{1}
consume(move resource)
```

The moved variable cannot be used again.

Destruction does not automatically recurse into by-value fields. Resource
owners should normally keep owned resources behind pointers and delete them
in their own destructor.

Automatic destruction happens when control leaves the declaring block,
including `return`, `break`, and `continue`. Only a type's own destructor is
called automatically; merely containing a destructible field does not make
that field clean itself up.

## Current allocation model

Strings, dynamic arrays, maps, closures, and similar runtime values use a
process-lifetime arena. Their old allocations are not reclaimed before the
program exits. `new` and `delete` use a separate, individually freed heap.

Try:

- [`pointers.llx`](../examples/pointers/pointers.llx)
- [`destructors.llx`](../examples/destructors/destructors.llx)

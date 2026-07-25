# Structs, enums, and match

## Structs

Structs group named fields:

```go
struct Point {
    x int
    y int
}

p := Point{x: 2, y: 3}
```

Composite literals may be positional (`Point{2, 3}`) or keyed
(`Point{x: 2}`). Keyed literals may omit fields.

Methods are declared outside the struct. `this` is the current value, and
methods always modify it by reference:

```go
func (Point) move(dx int, dy int) {
    this.x += dx
    this.y += dy
}
```

## Constructors

Constructors live inside the struct:

```go
struct Point {
    x int
    y int

    constructor(x int, y int) {
        this.x = x
        this.y = y
    }
}

p := Point(2, 3)
```

Constructors may be overloaded by argument count only. `Point{2, 3}` is a
raw composite literal and does not call a constructor.

Structs may also declare one destructor. That affects copying and ownership;
see [Ownership, move, and pointers](memory-and-pointers.md).

## Enums

Enums are tagged unions. Variants may have no data, positional data, or
named data:

```go
enum Shape {
    Point,
    Circle(f64),
    Rectangle { width f64, height f64 },
}

a := Shape.Point
b := Shape.Circle(2.0)
c := Shape.Rectangle{width: 3.0, height: 4.0}
```

Enums can have methods, recursive pointer variants, and one destructor. An
enum is comparable or printable only when every variant's stored data
supports that operation.

## Match statements

Enum matches must cover every variant unless they include `_`:

```go
match shape {
    Shape.Circle(radius) => {
        print(radius)
    }
    Shape.Rectangle{width: w, height: h} => {
        print(w * h)
    }
    Shape.Point => {
        print(0)
    }
}
```

`match` also works with integers, strings, and booleans. These value matches
always need a wildcard arm:

```go
match status {
    200, 201 => {
        print("success")
    }
    _ => {
        print("other")
    }
}
```

Floating-point, pointer, struct, array, map, and function values cannot be
value-match subjects. One arm runs, there is no fallthrough, and no `break`
is needed.

## Match expressions

A match can produce a value. Use a short expression or `yield` from a block:

```go
label := match score {
    0 => "none"
    1, 2 => {
        yield "some"
    }
    _ => "many"
}
```

Every reachable path in a block arm must `yield`, unless it exits the
enclosing function or loop. `return` still returns from the function; it
does not produce the match value.

Try:

- [`features.llx`](../examples/features/features.llx)
- [`constructors.llx`](../examples/constructors/constructors.llx)
- [`enums.llx`](../examples/enums/enums.llx)
- [`match_expr.llx`](../examples/match_expr/match_expr.llx)

Next: [Functions and generics](functions-and-generics.md)

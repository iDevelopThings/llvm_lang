# Structs

A struct groups named fields:

```go
struct Point {
    x f64
    y f64
}

func (Point) LengthSquared() f64 {
    return this.x * this.x + this.y * this.y
}

point := Point{x: 3.0, y: 4.0}
print(point.LengthSquared())
```

Methods receive `this` automatically and may change its fields.

Struct literals may be keyed or positional:

```go
origin := Point{x: 0.0, y: 0.0}
alsoOrigin := Point{0.0, 0.0}
```

Cross-package code may use positional literals only when every field is
public. Keyed literals may name accessible public fields.

## Constructors

Name a method `constructor` to validate or calculate fields:

```go
struct User {
    name string

    constructor(name string) {
        this.name = name
    }
}

user := User("Ada")
```

Constructors may be overloaded by parameter count. `User{...}` is still a
direct literal and does not call a constructor.

## Operator overloads

Operator declarations define arithmetic for the left-hand struct:

```go
struct Point {
    x int
    y int

    operator +(other Point) Point {
        return Point{this.x + other.x, this.y + other.y}
    }

    operator -() Point {
        return Point{-this.x, -this.y}
    }
}
```

Binary `+`, `-`, `*`, `/` and unary `-` may be overloaded. Dispatch uses only
the left operand. Equality stays structural and cannot be overloaded.

Destructors and non-copyable structs are covered in
[ownership and `move`](ownership.md).

[Previous: Collections](collections.md) ·
[Next: Enums](enums.md)

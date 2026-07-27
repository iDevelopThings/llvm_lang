# Generics

Put type parameters in brackets:

```go
func Identity[T](value T) T {
    return value
}

first := Identity(42)
second := Identity[string]("hello")
```

Arguments are usually inferred. Use explicit type arguments when inference
has no value to work from.

## Generic structs

```go
struct Pair[A, B] {
    first A
    second B
}

func (Pair[A, B]) Swap() Pair[B, A] {
    return Pair[B, A]{this.second, this.first}
}

pair := Pair[int, string]{1, "one"}
```

Methods automatically know the receiver's type parameters. A method may add
its own inferred parameters:

```go
func (Pair[A, B]) Replace[U](value U) Pair[U, B] {
    return Pair[U, B]{value, this.second}
}
```

## What “generic” means here

Type parameters are unconstrained. The compiler checks each concrete
instantiation, so an operation is accepted only for types that support it:

```go
func Twice[T](value T) T {
    return value + value
}
```

`Twice(2)` and `Twice("a")` work; a type without `+` fails at its call site.

Generic functions cannot be stored as values before they are instantiated.
Enums cannot currently be generic, and method type arguments cannot be
written explicitly.

[Previous: Function values and lambdas](function-values.md) ·
[Next: Packages and imports](packages.md)

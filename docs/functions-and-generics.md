# Functions and generics

## Multiple return values

Use multiple returns for results that may fail:

```go
func divide(a int, b int) (int, bool) {
    if b == 0 {
        return 0, false
    }
    return a / b, true
}

result, ok := divide(10, 2)
```

The results must be unpacked immediately; there is no tuple type.

The right side of a multi-return unpack must be one function call, or a map
lookup for the two-result `value, found` form. It cannot be stored as one
value or forwarded as a group of arguments.

Parallel assignment is also supported:

```go
a, b := 1, 2
a, b = b, a
```

## Function values

Free functions are values:

```go
func double(x int) int {
    return x * 2
}

func apply(fn func(int) int, value int) int {
    return fn(value)
}

result := apply(double, 21)
```

Bound method values such as `point.move` are not supported.

## Lambdas and closures

Function literals use the same `func` syntax:

```go
factor := 3
scale := func(value int) int {
    return value * factor
}
```

Captured variables are shared by reference. A closure can outlive the
function that created it.

Loop-header variables get a fresh captured value per iteration, matching Go
1.22 and later. A lambda cannot capture a method's `this`.

## Generics

Functions and structs can declare type parameters:

```go
func First[T](items []T) T {
    return items[0]
}

struct Pair[A, B] {
    first A
    second B
}
```

Type arguments are usually inferred:

```go
value := First([]int{10, 20})
```

Write them explicitly when inference has no argument to inspect:

```go
func Empty[T]() []T {
    return make([]T, 0)
}

items := Empty[string]()
```

Generics are unconstrained. Their bodies are checked separately for each
concrete use, so an operation only needs to work for the types you actually
instantiate.

Method type parameters are inference-only; `value.Method[int]()` is not
supported. Generic enums and uninstantiated generic function values are also
not supported.

Try:

- [`multireturn.llx`](../examples/multireturn/multireturn.llx)
- [`multi_assign.llx`](../examples/multi_assign/multi_assign.llx)
- [`first_class_functions.llx`](../examples/first_class_functions/first_class_functions.llx)
- [`closures.llx`](../examples/closures/closures.llx)
- [`generics.llx`](../examples/generics/generics.llx)

Next: [Packages and the standard library](packages-and-stdlib.md)

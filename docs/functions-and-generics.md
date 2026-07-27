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

## Variadic parameters

A function's last parameter may take any number of trailing arguments by
marking it with `...`:

```go
func Sum(nums ...int) int {
    total := 0
    for i := range nums {
        total = total + nums[i]
    }
    return total
}

Sum()           // 0
Sum(1)          // 1
Sum(1, 2, 3)    // 6
```

Inside the function, `nums` is an ordinary `[]int` - see
[Dynamic arrays](collections.md#dynamic-arrays) for what that means (`len`,
indexing, `range`, passing it on to another `[]int` parameter, all work
unchanged). Each collected argument is checked against the element type with
this language's usual no-implicit-conversion rule - except a `...Any`
parameter, where each argument boxes automatically (see
[Any](advanced-features.md#any)).

Forward an existing slice instead of collecting a new one with a trailing
`...` after the argument:

```go
parts := []string{"a", "b", "c"}
joined := Join(",", parts...)
```

`parts...` passes `parts` itself as the variadic argument; its type must be
exactly `[]string` for a `...string` parameter. `...` is only legal on a
call's last argument, and only when the callee's own last parameter is
variadic.

Only the last parameter may be variadic. A variadic function referenced as a
bare value (`f := Sum`, not `Sum(...)`) is not supported - see
[Current limitations](current-limitations.md).

Try: [`variadic.llx`](../examples/variadic/variadic.llx)

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

# Functions

Parameters place the name before the type. The return type follows the
parameter list:

```go
func add(left int, right int) int {
    return left + right
}
```

Every path in a non-`void` function must return or stop in a provably endless
loop.

## Multiple return values

A function may return several values:

```go
func divide(value int, by int) (int, int) {
    return value / by, value % by
}

quotient, remainder := divide(17, 5)
```

Multiple returns are destructured immediately. They are not tuple values and
cannot be forwarded with `return otherCall()`.

Parallel assignment evaluates the right side before changing the left:

```go
left, right = right, left
```

## Variadic parameters

Put `...` on the final parameter:

```go
func sum(values ...int) int {
    total := 0
    for _, value := range values {
        total += value
    }
    return total
}

print(sum(1, 2, 3))
```

Inside the function, `values` is `[]int`. Spread an existing slice into the
last argument with `values...`:

```go
numbers := []int{4, 5, 6}
print(sum(numbers...))
```

`...Any` parameters also accept ordinary values and box them automatically.

Methods, function values, lambdas, and generic functions build on this
syntax in later pages.

[Previous: Control flow](control-flow.md) ·
[Next: Collections](collections.md)

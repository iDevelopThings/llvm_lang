# Function values and lambdas

A function type describes its parameters and result:

```go
func(int, int) int
func(string)
func() (int, bool)
```

Top-level functions can be stored and called through that type:

```go
func add(a int, b int) int {
    return a + b
}

var operation func(int, int) int = add
print(operation(2, 3))
```

Bound methods are not function values. Call them through their receiver.

## Lambdas

A lambda uses ordinary `func` syntax without a name:

```go
double := func(value int) int {
    return value * 2
}

print(double(4))
```

Lambdas capture visible locals by reference:

```go
total := 0
add := func(value int) {
    total += value
}

add(5)
print(total)
```

Captured storage remains available when a lambda is returned from its
enclosing function. A lambda cannot capture a method's `this`.

Each iteration variable created by a `for` loop has fresh storage, so lambdas
created on different iterations see their own value.

Variadic functions are not currently assignable as function values.

[Previous: Pointers](pointers.md) ·
[Next: Generics](generics.md)

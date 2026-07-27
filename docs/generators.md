# Generators

A generator yields values lazily into a `range` loop:

```go
func Countdown(from int) yield int {
    value := from
    for value > 0 {
        yield value
        value--
    }
}

for value := range Countdown(3) {
    print(value)
}
```

Use `return` without a value to stop early.

The loop may omit the binding, but cannot ask for an index and value. A
generator produces only its yielded value:

```go
for range Countdown(3) {
    print("tick")
}
```

Generators are currently consumed only as a direct call in `range`. They
cannot be stored, passed as function values, declared as methods, or resumed
manually. A generator has one yield type and cannot contain a nested
generator.

Generators are pull-style iteration. For code that pauses and is resumed by
a caller, use a [coroutine](coroutines.md).

Try [`generators.llx`](../examples/generators/generators.llx).

[Previous: Testing](testing.md) ·
[Next: Coroutines](coroutines.md)

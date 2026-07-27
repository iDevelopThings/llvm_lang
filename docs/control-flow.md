# Control flow

Conditions are boolean expressions. Parentheses are optional.

```go
if score >= 90 {
    print("excellent")
} else if score >= 60 {
    print("pass")
} else {
    print("retry")
}
```

## Shorthand `if`

Use `:` for a single statement with no `else`:

```go
if value > 0: print(value)
if failed: return
```

Use braces when you need several statements or an `else`.

## Loops

Use `for` for every loop shape:

```go
for ready {
    work()
}

for {
    tick()
    if finished {
        break
    }
}

for i := 0; i < 10; i++ {
    print(i)
}
```

`break` leaves the nearest loop. `continue` starts its next iteration.

## Range loops

Range over arrays, dynamic arrays, slices, maps, and
[generators](generators.md):

```go
for index, value := range values {
    print(index)
    print(value)
}

for key := range scores {
    print(key)
}
```

With one variable, arrays and slices produce the index; maps produce the key;
generators produce the yielded value. Range declarations always use `:=`.

Use [`match`](match.md) when one value selects between several cases.

[Previous: Types and conversions](types-and-conversions.md) ·
[Next: Functions](functions.md)

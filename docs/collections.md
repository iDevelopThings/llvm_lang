# Collections

## Fixed arrays

`[N]T` stores exactly `N` values:

```go
numbers := [3]int{10, 20, 30}
numbers[0] = 5
print(len(numbers))
```

`N` must be a positive integer literal.

## Dynamic arrays

`[]T` is a growable array:

```go
numbers := []int{10, 20}
numbers = append(numbers, 30)
print(numbers)
```

Create a zero-filled dynamic array with `make`:

```go
a := make([]int, 3)
b := make([]int, 0, 8)
```

`append` adds one value at a time and returns the updated array.

Assignment shares a dynamic array's backing storage. Dynamic arrays can be
printed, but not compared with `==` or `!=`.

## Slicing

Slicing does not copy the elements. The result shares storage with the
original value.

```go
numbers := []int{10, 20, 30, 40}
middle := numbers[1:3]
middle[0] = 99
print(numbers[1]) // 99
```

The forms `x[a:b]`, `x[:b]`, `x[a:]`, and `x[:]` work on dynamic arrays,
fixed arrays, and strings. An omitted bound uses the start or length.

An explicit high bound may extend a dynamic array up to its capacity. Slicing
a fixed array produces a dynamic array that shares its storage.

A single string index (`s[i]`, not a range) reads one byte as a `u8`:

```go
s := "hello"
print(s[0]) // 104
```

This is read-only - `s[i] = x` and `&s[i]` are both rejected, since strings
are immutable. An out-of-range `i` traps at runtime, same as an array index.

## Maps

Create maps with `make`, then use index syntax to read or write:

```go
scores := make(map[string]int)
scores["Ada"] = 10

score, found := scores["Ada"]
remove(scores, "Ada")
print(len(scores))
```

A missing key returns the value type's zero value. The second result tells
you whether the key existed.

Maps share their table when assigned or passed. Keys must be comparable, and
both key and value types must be copyable.

A map element has no stable address. `&scores["Ada"]`, `scores["Ada"]++`,
and compound assignment to a map element are rejected; read, calculate, and
write it back.

There is no map literal yet. Map iteration order is unspecified.

## Range loops

Use `range` to visit the elements:

```go
for i, value := range numbers {
    print(i)
    print(value)
}

for key, value := range scores {
    print(key)
    print(value)
}
```

With one name, arrays and slices produce the index; maps produce the key. See
[control flow](control-flow.md#range-loops) for the other forms.

Try:

- [`dynamic_arrays.llx`](../examples/dynamic_arrays/dynamic_arrays.llx)
- [`slicing.llx`](../examples/slicing/slicing.llx)
- [`word_freq.llx`](../examples/word_freq/word_freq.llx)
- [`range.llx`](../examples/range/range.llx)

[Previous: Functions](functions.md) ·
[Next: Structs](structs.md)

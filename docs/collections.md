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

Dynamic arrays are headers pointing at shared backing storage. Assignment
does not clone their elements. They can be printed, but cannot be compared
with `==` or `!=`.

An invalid index, slice bound, length, or capacity prints the values involved
and then aborts the process. It is not a recoverable exception.

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
fixed arrays, and strings. Slicing a fixed array produces a dynamic array.

An explicit high bound may extend a dynamic array up to its capacity.
Strings are bounded by their length. A fixed array must be addressable
because the result keeps pointing into its storage.

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

Maps are reference values: assigning or passing one keeps sharing the same
table. Keys must be comparable, so dynamic arrays, maps, and function values
cannot be keys.

A map element has no stable address. `&scores["Ada"]`, `scores["Ada"]++`,
and compound assignment to a map element are rejected; read, calculate, and
write it back.

There is no map literal yet. Map iteration order is unspecified.

## Range loops

Use two names when you need both the index/key and value:

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

With one name, arrays provide the index and maps provide the key. A loop can
also omit both names:

```go
for range numbers {
    print("one item")
}
```

The two-name form requires fresh names with `:=`; an `=` reuse form is not
supported.

There is no special blank identifier yet. Some examples use `_` as an
ordinary, intentionally-unused name, but it is not a general discard
operator.

Strings and bare integers cannot be ranged over yet.

Try:

- [`dynamic_arrays.llx`](../examples/dynamic_arrays/dynamic_arrays.llx)
- [`slicing.llx`](../examples/slicing/slicing.llx)
- [`word_freq.llx`](../examples/word_freq/word_freq.llx)
- [`range.llx`](../examples/range/range.llx)

Next: [Structs, enums, and match](structs-enums-and-match.md)

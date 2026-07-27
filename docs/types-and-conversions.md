# Types and conversions

The built-in value types are:

| Kind | Types |
| --- | --- |
| Signed integers | `i8`, `i16`, `i32`, `i64`, `int` |
| Unsigned integers | `u8`, `u16`, `u32`, `u64` |
| Floating point | `f32`, `f64` |
| Other | `bool`, `string`, `void` |

`int` is an alias for `i32`.

## Typed values do not convert automatically

Convert explicitly when two typed numeric values differ:

```go
var small i16 = 12
large := i64(small)
ratio := f64(large) / 10.0
```

Numeric literals adapt to their context, so this works:

```go
var count i64 = 10
count = count + 2
```

An integer literal without a context becomes `int`. A floating-point literal
becomes `f64`.

## Operators

```text
++  --  +  -  *  /  %
+=  -=  *=  /=
==  !=  <  <=  >  >=
&&  ||  !
&  |  ^  <<  >>
```

Operands normally need the same type. Integer division truncates toward zero.
Comparing integers of different widths or signedness is an error.

Assignment targets a variable, field, index, or dereferenced pointer.
Compound assignment and `++`/`--` update that same storage.

Strings support `+`, equality, and ordered comparison. Arrays, structs, and
enums support equality only when everything stored inside them is comparable.
Dynamic arrays (including slices), maps, functions, and `Any` are not
comparable.

## Strings

`len(text)` returns its byte length. Indexing returns a `u8`; slicing returns
a new `string`.

```go
word := "hello"
first := word[0]
tail := word[1:]
```

Use the [`std:strings`](standard-library.md#stdstrings) package for searching,
splitting, casing, and number formatting.

[Previous: Program basics](basics.md) ·
[Next: Control flow](control-flow.md)

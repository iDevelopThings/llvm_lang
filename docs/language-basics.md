# Language basics

llvm_lang looks deliberately familiar if you know Go, C, or a similar
language.

## Variables and types

Use `:=` inside a function when the type is obvious:

```go
name := "Ada"
score := 42
```

Use `var` when you want to state the type or start with its zero value:

```go
var count i64 = 10
var ready bool
var inferred = "hello"
```

Built-in value types:

| Kind | Types |
| --- | --- |
| Signed integers | `i8`, `i16`, `i32`, `i64` |
| Unsigned integers | `u8`, `u16`, `u32`, `u64` |
| Floating point | `f32`, `f64` |
| Other | `bool`, `string` |

`int` is an alias for `i32`. Numeric types do not mix implicitly:

```go
small := 10
wide := i64(small)
```

Numeric literals start untyped. They take the surrounding numeric type, or
default to `int` and `f64` when there is no type context:

```go
var small i8 = 5
whole := 5       // int
decimal := 5.0   // f64
```

Explicit numeric conversions use call syntax. Float-to-integer conversions
truncate toward zero.

## Conditions

Conditions must be `bool`. There are no truthy or falsy values.

```go
if score >= 40 {
    print("pass")
} else {
    print("try again")
}
```

`else if` chains work normally. For one statement, there is also a shorthand
form with no `else`:

```go
if score >= 40: print("pass")
```

## Loops

`for` covers the usual loop forms. There is no separate `while`.

```go
for i := 0; i < 3; i++ {
    print(i)
}

for count > 0 {
    count--
}

for {
    break
}
```

Use `break` and `continue` as expected. See [Collections](collections.md) for
`for ... range`.

## Operators

The common arithmetic, comparison, Boolean, and assignment operators are
available:

```text
+  --  +  -  *  /  %
==  !=  <  <=  >  >=
&&  ||  !
=  -=  *=  /=
&  |  ^
```

`+` also joins strings:

```go
message := "Hello, " + name
```

Assignment targets may be variables, fields, indexes, or dereferenced
pointers:

```go
value = 1
point.x = 2
items[0] = 3
*pointer = 4
```

## Functions

```go
func add(a int, b int) int {
    return a + b
}
```

A function with a return type must return on every possible path. Functions
without a return type may fall through. A complete `if`/`else`, exhaustive
`match`, or unbroken infinite loop can also prove that a path never falls
through.

## Simple built-ins

`print(value)` writes one value followed by a newline. It accepts numbers,
booleans, strings, pointers, arrays, dynamic arrays, and printable structs or
enums.

`args()` returns the command-line arguments as `[]string`. A standalone
program built with `-o` receives its real arguments. A JIT-run program
currently receives an empty array.

Other built-ins are introduced with their topics:

- Collections: `make`, `append`, `len`, `remove`
- Coroutines: `resume`, `done`
- Pointers: `nil`

## Top-level code

A file may contain imports, global variables, functions, structs, and enums.
Executable statements belong inside functions.

Global initializers run before `main`, in declaration order:

```go
func startingScore() int {
    return 40 + 2
}

var score int = startingScore()
```

Next: [Collections](collections.md)

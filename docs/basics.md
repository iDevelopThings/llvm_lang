# Program basics

A small program is a package containing functions. Execution starts at
`main`.

```go
func main() {
    name := "Ada"
    var age int = 36
    print(name)
    print(age)
}
```

Use `:=` when the type is obvious. Use `var` or an explicit type when you
want to state it:

```go
var ready bool
var count i64 = 10
message := "hello"
```

## Top-level declarations

A source file may contain imports, global variables, functions, structs,
enums, and [`tests {}` blocks](testing.md). Statements belong inside a
function.

Globals may use expressions and function calls:

```go
var width = 20
var area = width * loadHeight()
```

Global initializers run in source order. Do not read a later global from an
earlier initializer.

## Printing

`print(value)` accepts one value:

```go
print("hello")
print(42)
print([3]int{1, 2, 3})
```

Numbers, booleans, strings, pointers, fixed and dynamic arrays, structs, and
enums can be printed when everything inside them is printable. Maps and
function values cannot.

## Command-line arguments

Declare `main(args []string)` when the program needs arguments:

```go
func main(args []string) {
    print(len(args))
}
```

`main` may return nothing or an `int` exit code. JIT execution currently
passes an empty argument list.

[Previous: Install and run](getting-started.md) ·
[Next: Types and conversions](types-and-conversions.md)

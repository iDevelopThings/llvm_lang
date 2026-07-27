# Current language limits

This is a quick list of deliberate limits, not a roadmap.

## Syntax and types

- `if condition: statement` has no `else`; use braces when you need one.
- Fixed-array sizes must be positive integer literals.
- Numeric types do not convert implicitly.
- There is no tuple type or general blank identifier.
- Dynamic arrays, maps, and functions are not comparable.
- A struct field or method may be named with a reserved word (`move`,
  `new`, ...), but a keyed composite literal can't construct it that way
  (`Point{move: 1}`) - use a positional literal instead.
- Operator overloading only covers binary `+ - * /` and unary `-`; `==`,
  `!=`, comparisons, bitwise, and logical operators are not overloadable.
  Only the left operand is ever checked for a matching overload - `scalar *
  vector` (the struct on the right) does not work, only `vector * scalar`.

## Any

- Boxing is limited to scalar types, pointers, structs, arrays (fixed or
  dynamic), maps, and enums - a function/cfunc value or a non-copyable type
  cannot be boxed into `Any`. A struct or array is boxable only if every one
  of its own field/element types is, and an enum only if every one of its
  own variants' every associated-data type is, all recursively - `Any`
  itself is one such unboxable nested type (see "Any" in `LANGUAGE.md`).
- A boxed map has no entry reflection - `AnyFields` yields zero iterations
  for it, and `AnyAs[T]` only checks that `T` is a map, not its key/value
  types. Iterating a boxed map's own key/value pairs is not supported.
- A boxed enum's `AnyFields` walks its own *active variant's* associated
  data - correct for a directly boxed enum value (`Any(someEnumValue)`).
  When an enum value is reached instead as a struct field or array element,
  the field/element's own descriptor is built once for the whole struct/
  array type, before any specific value (and its live discriminant) is in
  hand - this round doesn't re-select the active variant at the point a
  field is actually walked, so `AnyName`/`AnyFields` called directly on
  that field's own `Any` (bypassing `AnyAs[EnumType]` first) reports the
  enum's own type name with zero fields rather than the real active
  variant's. Not an architectural limit - a scope decision for this round.
- Boxing an `Any` into another `Any` is legal - a cheap no-op copy, not an
  error.
- `Any` is neither comparable (`==`/`!=`) nor printable (`print`), and
  cannot cross an `extern func` signature.
- `AnyKind` returns a raw `i32` ordinal, not a named/nameable enum value.
- `AnyAs[T]` always needs an explicit type argument; `T` is never inferred.
- `AnyLen`/`AnyIndex` on a non-array `Any` return a harmless `0`/
  `(zero Any, false)` rather than a compile-time error - only the argument's
  static type (`Any`) is checked at compile time, not the boxed kind itself.

## Collections

- `append` adds one element per call.
- Three-index slicing (`items[a:b:c]`) is not supported.
- Maps have no literal syntax, and map elements are not addressable.
- `range` supports arrays, dynamic arrays, maps, and direct generator calls;
  not strings, integers, or user-defined iterators.
- Range bindings must be newly declared with `:=`.

## Functions and types

- Only free functions have first-class function values; bound method values
  are not supported. A variadic function cannot be used as a value either -
  only called directly.
- A lambda or constructor's own last parameter may be written `...T`, but
  gets no collect/spread call-site sugar - it's just an ordinary `[]T`
  parameter there, unlike a named function's variadic parameter.
- Lambdas cannot capture a method's `this`.
- Generic enums are not supported.
- Explicit type arguments work on free-function calls, but not method calls.
- Generic bodies are checked only when instantiated.

## Ownership and memory

- Arena-backed runtime allocations are reclaimed only when the process exits.
- `new` and `delete` use a separate manually managed heap.
- Destruction does not automatically recurse into by-value fields.
- There is no borrow checker, garbage collector, reference counting, or
  general protection from pointer aliasing mistakes.
- Dynamic arrays cannot contain non-copyable values.

## Match, generators, and coroutines

- Value matching supports integers, strings, and booleans—not floats or
  arbitrary comparable values.
- An enum-match arm can contain only one variant pattern.
- Generators produce one value at a time and can only be consumed directly
  by a range loop.
- Async functions cannot be methods, lambdas, or directly await another
  coroutine; a coroutine-to-coroutine call/await is still out of scope.
- Coroutines require optimization and cannot be compiled with `-no-opt`.

## Packages, interop, and tools

- Imports use relative paths, or the `std:`/`lib:` schemes; `lib:` isn't
  backed by anything yet, and imports cannot be aliased.
- The compiler and C interop currently target Windows with mingw64.
- FFI has no external variables, variadic functions, or symbol aliases.
- JIT-run programs see an empty `args()` array; standalone executables receive
  real command-line arguments.
- The language server re-analyzes a whole package per edit - no incremental
  parsing yet.
- `llvmc -test` only discovers `TestXxx` functions (standalone or inside a
  `tests{}` block) belonging to the entry package itself - an imported
  package's own tests never run as a side effect of being imported.


# Current language limits

This is a quick list of deliberate limits, not a roadmap.

## Syntax and types

- `if condition: statement` has no `else`; use braces when you need one.
- Fixed-array sizes must be positive integer literals.
- Numeric types do not convert implicitly.
- There is no tuple type, variadic function syntax, argument spreading, or
  general blank identifier.
- Dynamic arrays, maps, and functions are not comparable.

## Collections

- `append` adds one element per call.
- Three-index slicing (`items[a:b:c]`) is not supported.
- Maps have no literal syntax, and map elements are not addressable.
- `range` supports arrays, dynamic arrays, maps, and direct generator calls;
  not strings, integers, or user-defined iterators.
- Range bindings must be newly declared with `:=`.

## Functions and types

- Only free functions have first-class function values; bound method values
  are not supported.
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
- Async functions have no result value and cannot be methods, lambdas, or
  directly await another coroutine.
- Coroutines require optimization and cannot be compiled with `-no-opt`.

## Packages, interop, and tools

- Imports use relative paths and cannot be aliased.
- The compiler and C interop currently target Windows with mingw64.
- FFI has no external variables, variadic functions, or symbol aliases.
- JIT-run programs see an empty `args()` array; standalone executables receive
  real command-line arguments.
- The language server has no completion or incremental parsing yet.


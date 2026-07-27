# Current limitations

This is the short list of limits most likely to affect a program. Exact
rejection rules live in [`LANGUAGE.md`](../LANGUAGE.md).

## Syntax and control flow

- Shorthand `if condition: statement` has no `else`.
- There is no tuple type or general blank identifier. Outside a `match`
  wildcard, `_` is an ordinary name.
- `range` does not support strings, integers, or user-defined iterators.
  Bindings must be newly declared with `:=`.
- Value `match` supports signed integers, strings, and booleans. Enum arms
  cannot combine several variants.

## Collections and types

- Fixed-array sizes must be positive integer literals.
- `append` adds one element at a time. Three-index slicing is unsupported.
- Maps have no literal syntax, and map elements are not addressable.
- Numeric types do not convert implicitly.
- Dynamic arrays (including slices), maps, functions, and `Any` are not
  comparable.
- A reserved word may be a struct field or method name, but cannot be named
  in a keyed literal. Use a positional literal.
- Only binary `+ - * /` and unary `-` can be overloaded. Dispatch checks the
  left operand only.

## Functions and generics

- Bound methods and variadic functions are not function values.
- A lambda or constructor parameter written `...T` behaves as an ordinary
  `[]T`; collect and spread syntax belongs to named function calls.
- Lambdas cannot capture a method's `this`.
- Generic enums are unsupported. Method type arguments are inferred and
  cannot be written explicitly.
- Generic bodies are checked when instantiated.

## Ownership and memory

- Arena-backed runtime allocations are reclaimed when the process exits.
  `new` and `delete` use a separate manual heap.
- Destruction does not automatically recurse into fields.
- There is no borrow checker, garbage collector, reference counting, or
  protection from unsafe pointer aliases.
- Dynamic arrays, maps, `Any`, coroutine arguments, and variadic storage
  cannot contain non-copyable values.

## Generators and coroutines

- Generators are consumed only by ranging directly over their call.
- Async functions cannot be methods or lambdas, and cannot directly `await`
  another coroutine.
- Async parameters and results must be copyable.
- Coroutines require optimization and cannot use `-no-opt`.

## `Any` and reflection

- Functions, C callbacks, and non-copyable values cannot be boxed.
- Maps expose no entries. `AnyAs[MapType]` and a `map[K]V` match arm confirm
  only that the value is a map, not its key and value types. Pointers are
  the same: `AnyAs[*Point]` and a `*Point` match arm accept any pointer,
  whatever it points to.
- A type-match arm cannot bind a fixed-size array (`v [3]int`); use the
  unbound form (`[3]int => { ... }`). Dynamic arrays (`v []int`) bind fine.
- A directly boxed enum exposes its active payload. An enum reached as a
  struct field or array element reports no payload fields; recover it with
  `AnyAs[EnumType]` and box that enum directly before inspecting its payload.
- `AnyKind` returns an `i32`, not a named enum.
- `TypeByName` searches declared structs and enums only.
- `AnyNew` rejects enums, `Any`, and non-copyable structs or arrays.

## Packages, FFI, and tools

- Imports have no aliases. The `lib:` namespace is reserved but not
  implemented.
- C interop targets 64-bit Windows through mingw64. It has no external
  variables, variadic functions, or symbol aliases.
- JIT programs receive no command-line arguments; built executables do.
- The language server re-analyzes the whole package after each edit.
- `llvmc -test` runs tests from the entry package only; `-test -all` runs a
  whole directory tree, but each package still as its own separate entry
  point, never via import (see
  [Testing a whole directory tree](testing.md#testing-a-whole-directory-tree)).

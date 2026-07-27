# Feature index

Use this page when you already know what you need. `—` means there is no
dedicated example yet.

## Core language

| Feature | Main page | Example |
| --- | --- | --- |
| Top-level declarations and globals | [Program basics](basics.md#top-level-declarations) | [`global_init`](../examples/global_init/global_init.llx) |
| Variables, inference, and zero values | [Program basics](basics.md) | [`features`](../examples/features/features.llx) |
| Built-in types and numeric conversions | [Types and conversions](types-and-conversions.md) | [`time_demo`](../examples/time_demo/time_demo.llx) |
| Operators, assignment, `++`, and `--` | [Types and conversions](types-and-conversions.md#operators) | [`features`](../examples/features/features.llx) |
| Brace and shorthand `if` | [Control flow](control-flow.md) | — |
| `for`, `break`, and `continue` | [Control flow](control-flow.md#loops) | [`features`](../examples/features/features.llx) |
| `print`, arguments, and `main` | [Program basics](basics.md#printing) | [`hello`](../examples/hello/hello.llx) |

## Functions

| Feature | Main page | Example |
| --- | --- | --- |
| Functions and missing-return checking | [Functions](functions.md) | — |
| Multiple returns | [Functions](functions.md#multiple-return-values) | [`multireturn`](../examples/multireturn/multireturn.llx) |
| Parallel assignment | [Functions](functions.md#multiple-return-values) | [`multi_assign`](../examples/multi_assign/multi_assign.llx) |
| Variadic parameters and spread calls | [Functions](functions.md#variadic-parameters) | [`variadic`](../examples/variadic/variadic.llx) |
| Function types and free-function values | [Function values](function-values.md) | [`first_class_functions`](../examples/first_class_functions/first_class_functions.llx) |
| Lambdas and capture | [Function values](function-values.md#lambdas) | [`closures`](../examples/closures/closures.llx) |
| Generic functions, structs, and methods | [Generics](generics.md) | [`generics`](../examples/generics/generics.llx) |

## Collections

| Feature | Main page | Example |
| --- | --- | --- |
| Fixed arrays | [Collections](collections.md#fixed-arrays) | [`features`](../examples/features/features.llx) |
| Dynamic arrays, `make`, `append`, and `len` | [Collections](collections.md#dynamic-arrays) | [`dynamic_arrays`](../examples/dynamic_arrays/dynamic_arrays.llx) |
| Array and string slicing | [Collections](collections.md#slicing) | [`slicing`](../examples/slicing/slicing.llx) |
| Maps, lookup, and `remove` | [Collections](collections.md#maps) | [`word_freq`](../examples/word_freq/word_freq.llx) |
| Collection and generator `range` | [Control flow](control-flow.md#range-loops) | [`range`](../examples/range/range.llx) |

## User-defined types and memory

| Feature | Main page | Example |
| --- | --- | --- |
| Structs, methods, and literals | [Structs](structs.md) | [`features`](../examples/features/features.llx) |
| Constructors | [Structs](structs.md#constructors) | [`constructors`](../examples/constructors/constructors.llx) |
| Operator overloading | [Structs](structs.md#operator-overloads) | [`operators`](../examples/operators/operators.llx) |
| Enums and payloads | [Enums](enums.md) | [`enums`](../examples/enums/enums.llx) |
| Enum and multi-pattern value matches | [`match`](match.md) | [`match_values`](../examples/match_values/match_values.llx) |
| Match expressions and `yield` | [`match`](match.md#match-expressions) | [`match_expr`](../examples/match_expr/match_expr.llx) |
| Destructors and scope cleanup | [Ownership](ownership.md) | [`destructors`](../examples/destructors/destructors.llx) |
| Non-copyable values and `move` | [Ownership](ownership.md#transfer-with-move) | — |
| Pointers, `nil`, `new`, and `delete` | [Pointers](pointers.md) | [`pointers`](../examples/pointers/pointers.llx) |

## Packages and library

| Feature | Main page | Example |
| --- | --- | --- |
| Multi-file packages | [Packages](packages.md) | [`multifile`](../examples/multifile/main.llx) |
| Imports, `std:`, `lib:`, and exports | [Packages](packages.md#import-a-package) | [`imports`](../examples/imports/app/main.llx) |
| Standard-library packages | [Standard library](standard-library.md) | [Library examples](examples.md#standard-library) |
| `tests {}` and `llvmc -test` | [Testing](testing.md) | [`test_demo`](../examples/test_demo/test_demo.llx) |

## Advanced features

| Feature | Main page | Example |
| --- | --- | --- |
| Generators and `yield` | [Generators](generators.md) | [`generators`](../examples/generators/generators.llx) |
| Coroutines, `await`, and results | [Coroutines](coroutines.md) | [`coroutines`](../examples/coroutines/coroutines.llx) |
| Coroutine scheduling | [Coroutines](coroutines.md) | [`scheduler_demo`](../examples/scheduler_demo/scheduler_demo.llx) |
| `Any` boxing and inspection | [`Any` and reflection](any-and-reflection.md) | [`any_demo`](../examples/any_demo/any_demo.llx) |
| Type IDs, lookup, construction, and mutation | [`Any` and reflection](any-and-reflection.md#type-registry) | [`type_registry_demo`](../examples/type_registry_demo/type_registry_demo.llx) |
| `extern func`, `cstring`, and `cfunc` | [C interop](ffi.md) | [`scope_timer`](../examples/scope_timer/scope_timer.llx) |

## Compiler and editor

| Feature | Main page | Example |
| --- | --- | --- |
| JIT and standalone builds | [Compiler](compiler.md#run-or-build) | [`hello`](../examples/hello/hello.llx) |
| LLVM IR output | [Compiler](compiler.md#inspect-llvm-ir) | — |
| Watch mode | [Compiler](compiler.md#watch-and-reload) | — |
| External library linking | [Compiler](compiler.md#link-libraries) | [`scope_timer`](../examples/scope_timer/scope_timer.llx) |
| Language server and editor setup | [Compiler](compiler.md#editor-support) | — |
| Tooling-only `stub func` declarations | [Compiler](compiler.md#tooling-only-stubs) | — |
| Exit codes | [Compiler](compiler.md#exit-codes) | — |

See [current limitations](current-limitations.md) for unsupported forms.

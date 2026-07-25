# Feature index

Use this page when you know what you are looking for. The main guide stays
short; this index makes the less-common features easy to find.

## Core language

| Feature | Documentation | Example |
| --- | --- | --- |
| Top-level declarations and global initialization | [Language basics](language-basics.md#top-level-code) | [`global_init`](../examples/global_init/global_init.llx) |
| Variables, zero values, and inferred types | [Language basics](language-basics.md#variables-and-types) | [`features`](../examples/features/features.llx) |
| Numeric types, untyped literals, and conversions | [Language basics](language-basics.md#variables-and-types) | [`time_demo`](../examples/time_demo/time_demo.llx) |
| Operators, assignment, `++`, and `--` | [Language basics](language-basics.md#operators) | [`features`](../examples/features/features.llx) |
| Block and shorthand `if` | [Language basics](language-basics.md#conditions) | — |
| Three forms of `for`, `break`, and `continue` | [Language basics](language-basics.md#loops) | [`features`](../examples/features/features.llx) |
| `print`, `args`, and `main` | [Getting started](getting-started.md), [Language basics](language-basics.md#simple-built-ins) | [`hello`](../examples/hello/hello.llx) |
| Missing-return checking | [Language basics](language-basics.md#functions) | — |

## Data and collections

| Feature | Documentation | Example |
| --- | --- | --- |
| Fixed arrays and array-size rules | [Collections](collections.md#fixed-arrays) | [`features`](../examples/features/features.llx) |
| Dynamic arrays, `make`, `append`, and `len` | [Collections](collections.md#dynamic-arrays) | [`dynamic_arrays`](../examples/dynamic_arrays/dynamic_arrays.llx) |
| Array, dynamic-array, and string slicing | [Collections](collections.md#slicing) | [`slicing`](../examples/slicing/slicing.llx) |
| Maps, two-result lookup, `remove`, and key rules | [Collections](collections.md#maps) | [`word_freq`](../examples/word_freq/word_freq.llx) |
| Array and map range loops | [Collections](collections.md#range-loops) | [`range`](../examples/range/range.llx) |

## User-defined types and ownership

| Feature | Documentation | Example |
| --- | --- | --- |
| Structs, methods, and composite literals | [Structs](structs-enums-and-match.md#structs) | [`features`](../examples/features/features.llx) |
| Constructors | [Constructors](structs-enums-and-match.md#constructors) | [`constructors`](../examples/constructors/constructors.llx) |
| Destructors and automatic scope cleanup | [Destructors](memory-and-pointers.md#destructors) | [`destructors`](../examples/destructors/destructors.llx) |
| Non-copyable types and `move` | [Ownership and move](memory-and-pointers.md#move-semantics) | — |
| Pointers, `nil`, `new`, and `delete` | [Pointers](memory-and-pointers.md#pointers) | [`pointers`](../examples/pointers/pointers.llx) |
| Tagged-union enums | [Enums](structs-enums-and-match.md#enums) | [`enums`](../examples/enums/enums.llx) |
| Enum and value match statements | [Match statements](structs-enums-and-match.md#match-statements) | [`match_values`](../examples/match_values/match_values.llx) |
| Match expressions and `yield` | [Match expressions](structs-enums-and-match.md#match-expressions) | [`match_expr`](../examples/match_expr/match_expr.llx) |

## Functions

| Feature | Documentation | Example |
| --- | --- | --- |
| Multiple returns and immediate unpacking | [Multiple return values](functions-and-generics.md#multiple-return-values) | [`multireturn`](../examples/multireturn/multireturn.llx) |
| Parallel assignment | [Multiple return values](functions-and-generics.md#multiple-return-values) | [`multi_assign`](../examples/multi_assign/multi_assign.llx) |
| Function types and first-class free functions | [Function values](functions-and-generics.md#function-values) | [`first_class_functions`](../examples/first_class_functions/first_class_functions.llx) |
| Lambdas, closures, and capture rules | [Lambdas and closures](functions-and-generics.md#lambdas-and-closures) | [`closures`](../examples/closures/closures.llx) |
| Generic functions, structs, and methods | [Generics](functions-and-generics.md#generics) | [`generics`](../examples/generics/generics.llx) |

## Packages and advanced features

| Feature | Documentation | Example |
| --- | --- | --- |
| Multi-file packages | [Packages](packages-and-stdlib.md#packages) | [`multifile`](../examples/multifile/main.llx) |
| Relative imports and export rules | [Imports](packages-and-stdlib.md#imports) | [`imports`](../examples/imports/app/main.llx) |
| Standard-library packages | [Standard library](packages-and-stdlib.md#standard-library) | [Standard-library examples](examples.md#packages-and-standard-library) |
| Generator functions | [Generators](advanced-features.md#generators) | [`generators`](../examples/generators/generators.llx) |
| Suspend/resume coroutines | [Coroutines](advanced-features.md#coroutines) | [`coroutines`](../examples/coroutines/coroutines.llx) |
| Coroutine scheduling | [Coroutines](advanced-features.md#coroutines) | [`scheduler_demo`](../examples/scheduler_demo/scheduler_demo.llx) |
| `extern func` | [Calling C](advanced-features.md#calling-c) | [`scope_timer`](../examples/scope_timer/scope_timer.llx) |
| `cstring` conversions and `cfunc` callbacks | [Calling C](advanced-features.md#calling-c) | — |

## Compiler

| Feature | Documentation |
| --- | --- |
| Editor diagnostics, hover, and navigation | [Editor support](compiler.md#editor-support) |
| JIT execution | [Run with the JIT](compiler.md#run-with-the-jit) |
| Standalone executables | [Build an executable](compiler.md#build-an-executable) |
| Optimized or unoptimized LLVM IR | [Inspect LLVM IR](compiler.md#inspect-llvm-ir) |
| Language-level tests | [Run language tests](compiler.md#run-language-tests) |
| Hot reload | [Watch and reload](compiler.md#watch-and-reload) |
| External libraries | [Link libraries](compiler.md#link-libraries) |
| Process exit behavior | [Exit codes](compiler.md#exit-codes) |

See [Current language limits](current-limitations.md) for unsupported forms
and important runtime caveats.

For precise diagnostics and every rejected edge case, use the full
[`LANGUAGE.md`](../LANGUAGE.md) specification.

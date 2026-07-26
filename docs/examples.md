# Examples

Run a single-file example with:

```powershell
.\llvmc.exe .\examples\hello\hello.llx
```

Run a multi-file example by passing its directory:

```powershell
.\llvmc.exe .\examples\multifile
```

## Start here

| Example | Shows |
| --- | --- |
| [`hello`](../examples/hello/hello.llx) | The smallest program |
| [`features`](../examples/features/features.llx) | Variables, loops, arrays, structs, and methods |
| [`error`](../examples/error/error.llx) | An intentional type error and diagnostic |
| [`global_init`](../examples/global_init/global_init.llx) | Global initialization before `main` |

## Collections and assignment

| Example | Shows |
| --- | --- |
| [`dynamic_arrays`](../examples/dynamic_arrays/dynamic_arrays.llx) | `make`, `append`, `len`, and literals |
| [`slicing`](../examples/slicing/slicing.llx) | Shared array, dynamic-array, and string slices |
| [`range`](../examples/range/range.llx) | Array and map range loops |
| [`word_freq`](../examples/word_freq/word_freq.llx) | A practical string-to-count map |
| [`multi_assign`](../examples/multi_assign/multi_assign.llx) | Parallel assignment and swapping |
| [`multireturn`](../examples/multireturn/multireturn.llx) | Multiple return values |

## Types and control flow

| Example | Shows |
| --- | --- |
| [`constructors`](../examples/constructors/constructors.llx) | Constructor overloads and composite literals |
| [`operators`](../examples/operators/operators.llx) | Overloaded `* + -` on a Vector2-like struct |
| [`destructors`](../examples/destructors/destructors.llx) | Automatic cleanup and explicit deletion |
| [`pointers`](../examples/pointers/pointers.llx) | Addresses, dereferencing, `new`, `delete`, and `nil` |
| [`enums`](../examples/enums/enums.llx) | Tagged unions, methods, and exhaustive matching |
| [`match_values`](../examples/match_values/match_values.llx) | Matching integers, strings, and booleans |
| [`match_expr`](../examples/match_expr/match_expr.llx) | Matches that produce values |

## Functions and generic code

| Example | Shows |
| --- | --- |
| [`first_class_functions`](../examples/first_class_functions/first_class_functions.llx) | Passing and returning functions |
| [`closures`](../examples/closures/closures.llx) | Lambdas with captured state |
| [`generics`](../examples/generics/generics.llx) | Generic functions, structs, and methods |
| [`generators`](../examples/generators/generators.llx) | Yielding values into range loops |
| [`coroutines`](../examples/coroutines/coroutines.llx) | Suspend, resume, completion, and cleanup |

## Packages and standard library

| Example | Shows |
| --- | --- |
| [`multifile/main.llx`](../examples/multifile/main.llx), [`shapes.llx`](../examples/multifile/shapes.llx), [`util.llx`](../examples/multifile/util.llx) | One package split across three files |
| [`imports/app`](../examples/imports/app/main.llx), [`imports/mathutils`](../examples/imports/mathutils/mathutils.llx) | Relative imports and exported names |
| [`mathutil_demo`](../examples/mathutil_demo/mathutil_demo.llx) | `std:mathutil` |
| [`strings_demo`](../examples/strings_demo/strings_demo.llx) | `std:strings` |
| [`slices_demo`](../examples/slices_demo/slices_demo.llx) | `std:slices` |
| [`collections_demo`](../examples/collections_demo/collections_demo.llx) | `std:collections`; its `SlotMap` tests live in a `tests{}` block inside [`collections.llx`](../std/collections/collections.llx) itself, run via `llvmc -test std/collections` |
| [`time_demo`](../examples/time_demo/time_demo.llx) | `std:time` |
| [`scheduler_demo`](../examples/scheduler_demo/scheduler_demo.llx) | `std:scheduler` and coroutines |
| [`test_demo`](../examples/test_demo/test_demo.llx) | `std:test` and `llvmc -test`; intentionally fails |

## C interop

| Example | Shows |
| --- | --- |
| [`scope_timer`](../examples/scope_timer/scope_timer.llx) | Calling Windows APIs with `extern func` |


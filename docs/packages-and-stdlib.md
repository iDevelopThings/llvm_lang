# Packages and the standard library

## Packages

A directory is a package. Every `.llx` file directly inside it shares one
scope:

```text
app/
    main.llx
    helpers.llx
```

Compile either the directory or one file inside it:

```powershell
.\llvmc.exe .\app
.\llvmc.exe .\app\main.llx
```

Subdirectories are separate packages.

## Imports

Imports use paths relative to the file containing the import:

```go
import "../mathutils"

func main() int {
    return mathutils.Add(2, 3)
}
```

A path with no `./`/`../` prefix but a colon before its first `/`
(`std:mathutil`) is the one exception: it resolves against that scheme's
own root instead, independent of the importing file's location - see
"Standard library" below.

Imports must appear before other declarations. Their local name is the last
path segment (after the scheme, if any); import aliases are not supported.

Imports are file-scoped. Two files using the same package must each import
it.

Import cycles are rejected. The same imported package is loaded only once,
even when several paths in the import graph reach it.

Names beginning with an uppercase letter are visible to other packages:

```go
func Add(a int, b int) int { // exported
    return a + b
}

func double(x int) int { // package-private
    return x * 2
}
```

This rule also applies to types, methods, and fields. Code outside a package
cannot use a positional struct literal when that struct contains private
fields; use a keyed literal naming only exported fields.

## Standard library

The standard library is ordinary llvm_lang code under `std/`. Import it with
the `std:` scheme, not a relative path - it resolves against a `std/`
directory expected to sit right next to the running `llvmc`/`llvmc-lsp`
executable, so it works the same way regardless of where your own project
lives on disk.

| Package | Main features |
| --- | --- |
| `std:mathutil` | `Sqrt`, `Pow`, `Floor`, `Ceil`, `Fabs`, `Abs`, `Min`, `Max` |
| `std:strings` | Search, prefix/suffix, trim, split, ASCII case conversion, number formatting |
| `std:slices` | `Contains`, `IndexOf`, `Reverse`, `Map`, `Filter`, `Reduce` |
| `std:collections` | Generic `SlotMap` with generation-checked handles; `Len`, `Clear`, and `Values`/`Handles` generators to iterate live entries |
| `std:time` | Performance-counter timing |
| `std:scheduler` | Timer scheduler for coroutines |
| `std:vectors` | `Vector2`/`Vector3` with operator overloads (`+ - * /`, unary `-`), `Length`, `Normalize`, `Dot`, `Cross` (`Vector3`), `Distance`, `Lerp` |
| `std:rect` | Axis-aligned `Rect` (built on `std:vectors`): `Min`/`Max`/`Center`, `Contains`, `Intersects`, `Intersection`, `Union` |
| `std:test` | Assertions used by `llvmc -test` |

Example:

```go
import "std:strings"

func main() {
    print(strings.ToUpper("hello"))
}
```

Third-party packages (`lib:name`) are reserved for future use but not
implemented yet - importing one is a compile error today.

String helpers currently operate on ASCII bytes rather than Unicode text.
`F64ToString` uses four decimal places rather than a shortest round-tripping
format.

Try:

- [`examples/imports`](../examples/imports/app/main.llx)
- [`examples/multifile`](../examples/multifile/main.llx)
- [`strings_demo.llx`](../examples/strings_demo/strings_demo.llx)
- [`slices_demo.llx`](../examples/slices_demo/slices_demo.llx)
- [`collections_demo.llx`](../examples/collections_demo/collections_demo.llx)
- `SlotMap`'s own `llvmc -test` assertions live in a `tests{}` block at the
  end of [`std/collections/collections.llx`](../std/collections/collections.llx)
  itself - see [`compiler.md`](compiler.md#run-language-tests) for the
  `tests{}` construct. `std/vectors/vectors.llx` and `std/rect/rect.llx` do
  the same.

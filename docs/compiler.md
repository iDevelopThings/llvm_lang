# Compiler commands

## Editor support

`llvmc-lsp.exe` provides live diagnostics, hover information, go to
definition, find references, occurrence highlighting, a document outline,
folding ranges, completion, and semantic highlighting for `.llx` files.

- JetBrains IDEs: import the included
  [LSP4IJ template](../cmd/llvmc-lsp/lsp4ij-template/README.md).
- VS Code: use the included
  [development extension](../cmd/llvmc-lsp/vscode-extension/README.md).

Every instantiation of a generic counts as the same declaration: find
references and occurrence highlighting on `Sum[T]`'s declaration find every
`Sum(...)` call, and vice versa. The outline shows each declaration's own
signature or field list beside its name.

Hovering a struct also shows its real in-memory layout: total size and
alignment, each field's own byte offset, and how many bytes are spent on
alignment padding rather than a field's own data. Hovering a single field
instead shows which struct it belongs to and that one field's own
size/alignment/offset, including any padding spent before the next field.

Incremental parsing is not implemented yet - each edit re-analyzes the whole
package.

## Run with the JIT

```powershell
.\llvmc.exe .\path\to\program.llx
.\llvmc.exe .\path\to\package
```

Passing one file still compiles every `.llx` file in that file's directory.

## Build an executable

```powershell
.\llvmc.exe -o program.exe .\path\to\package
```

## Inspect LLVM IR

```powershell
.\llvmc.exe -emit-llvm .\path\to\program.llx
```

Add `-no-opt` to see the unoptimized IR. Coroutines cannot use `-no-opt`.
`-emit-llvm` cannot be combined with `-o`, `-watch`, `-l`, or `-L`.

## Run language tests

```powershell
.\llvmc.exe -test .\path\to\package
```

Tests are free functions named `TestXxx` with this exact shape:

```go
import "std:test"

func TestAdd(t *test.Runner) {
    t.AssertEqual(2 + 3, 5, "addition")
}
```

See [`test_demo.llx`](../examples/test_demo/test_demo.llx). It intentionally
contains one failing test.

Only tests in the entry package are discovered. A package defining its own
`main` conflicts with the generated test driver. No matching tests is a
usage error.

## Watch and reload

```powershell
.\llvmc.exe -watch .\path\to\package
```

Watch mode reloads changed source while keeping the JIT alive. By default it
calls `Init()` after a load and repeatedly calls `Frame() int`; a non-zero
result exits.

Use `-init Name` and `-tick Name` to choose other function names.

After a compile error, watch mode keeps running the last successful version.
`Init` must be safe to run again after every successful reload.

## Link libraries

`-L` adds a library directory and `-l` adds a library. Both flags may be
repeated and work with JIT or `-o`:

```powershell
.\llvmc.exe -L C:\libs -l physics .\app
.\llvmc.exe -o app.exe -L C:\libs -l physics .\app
```

## Exit codes

- `2`: invalid command or input path
- `1`: compilation, verification, JIT, or link failure
- otherwise: the JIT-run program's own `main` result

# Compiler and editor tools

Build the tools with `.\build.ps1`. This produces `llvmc.exe` and
`llvmc-lsp.exe`.

## Run or build

Run a file or package through the JIT:

```powershell
.\llvmc.exe .\path\to\program.llx
.\llvmc.exe .\path\to\package
```

Passing one file still compiles every `.llx` file in its directory.

Build a standalone executable:

```powershell
.\llvmc.exe -o program.exe .\path\to\package
```

The executable does not need Go, LLVM, or `llvmc` at runtime.

## Editor support

`llvmc-lsp.exe` provides live diagnostics, hover information, go to
definition, find references, occurrence highlighting, a document outline,
folding ranges, completion, and semantic highlighting for `.llx` files.

- JetBrains IDEs: import the included
  [LSP4IJ template](../cmd/llvmc-lsp/lsp4ij-template/README.md).
- VS Code: use the included
  [development extension](../cmd/llvmc-lsp/vscode-extension/README.md).

Struct and field hovers include their real size, alignment, offsets, and
padding. Generic references are grouped across their instantiations.

Incremental parsing is not implemented yet - each edit re-analyzes the whole
package.

## Inspect LLVM IR

```powershell
.\llvmc.exe -emit-llvm .\path\to\program.llx
```

Add `-no-opt` to see the unoptimized IR. Coroutines cannot use `-no-opt`.
`-emit-llvm` cannot be combined with `-o`, `-watch`, `-l`, or `-L`.

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

## Tooling-only stubs

`stub func` declarations exist only in `std/stubs.llx`. They give the
language server signatures for compiler built-ins such as `append` and
reflection helpers. They are not callable package functions and cannot be
written in ordinary source.

## Exit codes

- `2`: invalid command or input path
- `1`: compilation, verification, JIT, or link failure
- otherwise: the JIT-run program's own `main` result

Run language tests with [`llvmc -test`](testing.md).

[Previous: C interop](ffi.md) ·
[Back to the documentation map](README.md)

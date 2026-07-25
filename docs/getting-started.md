# Getting started

llvm_lang currently targets Windows and uses LLVM 22 through MSYS2's mingw64
toolchain.

## Build the compiler

Install the prerequisites in [`SETUP.md`](../SETUP.md), then run:

```powershell
.\build.ps1
```

This creates `llvmc.exe` and `llvmc-lsp.exe`.

## Run your first program

Create `hello.llx`:

```go
func main() {
    print("Hello, World!")
}
```

Run it with the JIT:

```powershell
.\llvmc.exe .\hello.llx
```

Or run the included version:

```powershell
.\llvmc.exe .\examples\hello\hello.llx
```

## What `main` returns

`main` may return nothing or an `int`.

```go
func main() int {
    return 5
}
```

When using the JIT, that value becomes `llvmc.exe`'s process exit code.
Falling off the end of `main` returns `0`.

## Make a standalone program

```powershell
.\llvmc.exe -o hello.exe .\hello.llx
.\hello.exe
```

The generated executable does not need Go, LLVM, or `llvmc` at runtime.

Next: [Language basics](language-basics.md)


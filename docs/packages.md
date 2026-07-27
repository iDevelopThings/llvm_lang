# Packages and imports

A package is one file or a directory of `.llx` files. Files in the same
directory share declarations:

```text
app/
├── main.llx
└── helpers.llx
```

Compile the directory:

```powershell
.\llvmc.exe .\app
```

Subdirectories are separate packages; discovery is not recursive.

## Import a package

Paths are relative to the importing file:

```go
import "../math"

func main() {
    print(math.Double(4))
}
```

The package name is the final path segment. Import aliases are not supported,
and each source file declares its own imports.

Names beginning with an uppercase letter are public across package
boundaries. Lowercase names stay private.

## Standard-library imports

Use the `std:` prefix:

```go
import "std:strings"
import "std:time"
```

The `lib:` prefix is reserved for a future installed-package namespace and
currently produces an error.

Import cycles are rejected. For a full multi-file example, see
[`examples/imports`](../examples/imports).

[Previous: Generics](generics.md) ·
[Next: Standard library](standard-library.md)

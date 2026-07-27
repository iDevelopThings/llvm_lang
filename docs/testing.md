# Testing

Run tests in a package with:

```powershell
.\llvmc.exe -test .\path\to\package
```

A test is a public free function named `TestXxx` with one
`*test.Runner` parameter:

```go
import "std:test"

func TestAdd(t *test.Runner) {
    t.AssertEqual(2 + 3, 5, "addition")
}
```

`std:test` provides equality, inequality, boolean, `nil`, and approximate
floating-point assertions.

## Testing a whole directory tree

Add `-all` to treat the path as a directory of packages instead of one
package: it recurses into every subdirectory, running each discovered
package's own suite independently (like `go test ./...`), rather than
compiling the whole tree as a single program:

```powershell
.\llvmc.exe -test -all std
```

A package with no `TestXxx` funcs is silently skipped. Each package prints
its own PASS/FAIL line, followed by one final summary:

```
=== PKG std\collections: PASS
=== PKG std\rand: PASS
=== SUMMARY: 6 package(s) run, 0 failed, took 1.32s
PASS
```

The process exits non-zero if any discovered package fails; one package's
failure never stops the rest of the walk from running. `-all` requires
`-test` and cannot be combined with `-o` (every discovered package would
otherwise collide on the same output path).

## Keep tests beside the code

A `tests {}` block is included only by `llvmc -test`:

```go
func add(a int, b int) int {
    return a + b
}

tests {
    import "std:test"

    func TestAdd(t *test.Runner) {
        t.AssertEqual(add(1, 1), 2, "1 + 1")
    }
}
```

The block may contain the same top-level declarations as a file. Put its
imports before its other declarations. Nested `tests` blocks are rejected.

Only tests in the entry package run. Imported packages do not run their own
tests, and a package with its own `main` conflicts with the generated test
driver.

[`test_demo.llx`](../examples/test_demo/test_demo.llx) is intentionally
failing, so it also shows the failure output.

[Previous: Standard library](standard-library.md) ·
[Next: Generators](generators.md)

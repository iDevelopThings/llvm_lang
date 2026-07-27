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

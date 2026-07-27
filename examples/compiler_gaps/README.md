# Compiler gaps

Minimal repros for language/compiler gaps hit while growing `std/`.
Each subdirectory is intentionally broken (or panics) until fixed.

| Dir | Symptom | Expected |
| --- | --- | --- |
| `cross_package_enum_variant` | `os.Error.Ok` → "Error is a type, not a value" | `pkg.Enum.Variant` constructs |
| `cross_package_enum_match` | `strings.IntParseResult.Ok(n)` arm does not bind `n` | Qualified enum match arms work |
| `package_qualified_var` | `lib.X` → codegen panic "identifier lib has no storage" | Read exported package `var` |
| `enum_method_on_construction` | `E.A.String()` → codegen panic "identifier E has no storage" | Method on fresh variant, or clean diagnostic |

Already fixed and removed: `cstring_nil`, `cstring_from_ptr`, `string_index`.

Run any case with `.\llvmc.exe examples\compiler_gaps\<dir>` (or the
`app` path for `package_qualified_var`).

Once fixed, delete or rewrite the case as a positive regression test.
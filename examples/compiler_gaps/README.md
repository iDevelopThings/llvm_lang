# Compiler gaps

Minimal repros for language/compiler gaps hit while growing `std/`.
Each subdirectory is intentionally broken (or panics) until fixed.

| Dir | Symptom | Expected |
| --- | --- | --- |
| `cross_package_enum_variant` | `os.Error.Ok` → "Error is a type, not a value" | `pkg.Enum.Variant` constructs |
| `cross_package_enum_match` | `strings.IntParseResult.Ok(n)` arm does not bind `n` | Qualified enum match arms work |
| `package_qualified_var` | `lib.X` → codegen panic "identifier lib has no storage" | Read exported package `var` |
| `enum_method_on_construction` | `E.A.String()` → codegen panic "identifier E has no storage" | Method on fresh variant, or clean diagnostic |
| `cstring_nil` | `cs == nil` rejected for `cstring` | `cstring` null-check like `*T` |
| `cstring_from_ptr` | `cstring(p)` rejected for `*u8` | Explicit `cstring(*u8)` / `*i8` |
| `string_index` | `s[0]` → "cannot index into string" | `s[i]` yields `u8`, bounds-checked |

Run any case with `.\llvmc.exe examples\compiler_gaps\<dir>` (or the
`app` path for `package_qualified_var`).

Once fixed, delete or rewrite the case as a positive regression test.
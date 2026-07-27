# Compiler gaps

Minimal repros for language/compiler gaps hit while growing `std/`.
Each subdirectory is intentionally broken (or panics) until fixed.

| Dir | Symptom | Expected |
| --- | --- | --- |

All seven original gaps are now fixed and turned into regression tests:
`cstring_nil`, `cstring_from_ptr`, `string_index`, `enum_method_on_construction`,
`cross_package_enum_variant`, `cross_package_enum_match`, and
`package_qualified_var` - see `src/sema/imports_test.go` and
`src/codegen/imports_test.go` (`TestImports_CrossPackageEnum*`/
`TestImports_PackageQualifiedVarRead`) for the last three.

Once fixed, delete or rewrite the case as a positive regression test.
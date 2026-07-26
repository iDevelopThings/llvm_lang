# enum_codegen

Generates container-based Go enums and optional matching TypeScript and Kotlin
types from small YAML specs. One spec describes an enum type, its underlying
representation, and its members with arbitrary per-member metadata; the
generator emits the constants, lookup tables, and helper methods so callers get
name/value/metadata resolution without hand-maintaining parallel maps.

## Usage

Point `-in` at a single spec or a directory of specs. Output locations are set
on the spec itself (`out`, `tsOut`, `ktOut`), so a `//go:generate` directive
only needs to name the input:

```go
//go:generate go run ../../cmd/enum_codegen -in ./enums
```

Then `go generate ./...`. See the root [`main.go`](../../main.go) directive and
the specs in [`src/enums`](../../src/enums).

Flags:

| flag    | meaning                                                                    |
| ------- | ------------------------------------------------------------------------- |
| `-in`   | a `.yml`/`.yaml` file, or a directory of them (default `.`)               |
| `-out`  | write the Go file to this exact directory, overriding the spec's `out`     |
| `-root` | write to `<root>/<package>`; for a central spec dir fanning out per package |

`-out` and `-root` are mutually exclusive.

## Spec format

```yaml
package: model          # required: Go package of the generated file
type: SlotName          # required: generated enum type name
underlying: byte        # optional: base type (default: int)
marshalText: false      # optional: emit MarshalText/UnmarshalText (default: false)
denseTable: false       # optional: use a dense Go metadata array (default: false)
aliasField: aliasOf     # optional: member key used for source-level aliases
container: SlotNames    # optional: container var name (default: <type> + "s")
out: ..                 # optional: Go output dir, relative to THIS file
tsOut: ../ui/slots.ts   # optional: TypeScript output file, relative to THIS file
ktOut: ../app/SlotName.kt # optional: Kotlin output file, relative to THIS file
ktPackage: dev.example.model # optional: Kotlin package (default: package)
values:
  - name: MainWeapon    # required: member identifier (PascalCase)
    wire: 0             # optional: const value (default: index, or name for string enums)
    title: "Main Weapon" # any extra keys become typed metadata columns
    desc: "RightHand"
  - name: MAX
    wire: 47
    sentinel: true      # const-only boundary; excluded from tables and iteration
  - name: Primary
    aliasOf: MainWeapon # const-only synonym for an earlier real member
```

### `underlying`

Any predeclared integer (`byte`, `rune`, `int8`…`int64`, `uint8`…`uintptr`),
float (`float32`/`float64`), or `string` type. The type is classified via
`go/types`, so all integer widths are handled uniformly and the correct `fmt`
verb is chosen for the `String()` fallback. Anything else is rejected at
generation time.

For string enums, `wire` values are emitted as quoted string literals and
default to the member `name` when omitted; for numeric enums they default to the
declaration index.

### `wire`

The constant's underlying value. Gaps are fine (the numbers come from the game
client, not a dense sequence). Negative values are allowed for signed types.

### `sentinel`

A member marked `sentinel: true` is emitted as a named constant only — a
boundary such as `MAX` used for bounds/counts. It is deliberately excluded from
the container, the `Infos`/`Values`/`ByName` tables, iteration, and the derived
TypeScript union, so it never appears as a "real" value.

### `aliasField`

Set `aliasField` to opt into source-level aliases and choose their per-member
key. An entry using that key emits one source-level constant equal to an earlier
real member. It has no wire slot and is excluded from containers, metadata,
values, iteration, `ByName`, and `Parse`:

```yaml
aliasField: aliasOf
values:
  - name: I32
  - name: Int
    aliasOf: I32
```

Keeping aliases out of name lookup is deliberate: aliases are source-code
conveniences, not additional serialized or parsed enum names.

The target must already be declared in the same enum and cannot be another
alias or a sentinel. An alias cannot also set `wire`, `sentinel`, or metadata.
The configured key cannot be `name`, `wire`, or `sentinel`.

When `aliasField` is omitted, no member key is reserved. In particular, an
existing metadata column named `alias` continues to work unchanged.

### `denseTable`

`denseTable: true` replaces the generated Go metadata map with a directly
indexed array. The live wire values must be exactly `0..n-1`, with no gaps or
duplicates. Aliases do not consume a default wire value; aliases and sentinels
do not occupy array slots.

Non-integer, negative, sparse, duplicate, and empty dense tables are rejected
during generation. Invalid runtime values remain safe because lookup methods
bounds-check before indexing.

### Metadata columns

Every key on a member other than `name`/`wire`/`sentinel` and the configured
`aliasField` becomes a metadata column. Its Go type is inferred from the YAML
scalar tag and widened across all members:

| YAML          | Go type    |
| ------------- | ---------- |
| `!!int`       | `int`      |
| `!!float`     | `float64`  |
| `!!bool`      | `bool`     |
| `!!str`       | `string`   |
| sequence      | `[]string` |

(YAML carries no integer-width information, so integer metadata is always `int`
— distinct from `underlying`, which is the wire type you name explicitly.)

### Declaring column types (`fields`)

Inference is the fallback; a top-level `fields:` block overrides it per column
when you need a specific type. Anything not listed stays inferred.

```yaml
fields:
  stat: StatId        # reference to another generated enum
  stats: "[]StatId"   # slice of references  (quote []… — YAML reads it as a flow seq otherwise)
  tier: int64         # a specific integer width
  weights: "[]float64"
```

A type is one of: a predeclared basic type (`string`, `bool`, any integer width,
`float32`/`float64`), the **type name of another enum** in the same generation
set (a reference), the **name of a Go struct** in the target package, or a slice
of any of these.

**Enum references** are the main reason to declare a type. `stat: fishingSpeed`
typed as `StatId` emits the compile-checked constant `StatIdFishingSpeed` (Go),
`StatIds.FishingSpeed` (TS), or `StatId.FishingSpeed` (Kotlin), honoring the
referenced enum's own `case` mode.
The generator resolves references across the whole `-in` set in a first pass, so
it **errors at generation time** if a value isn't a real member of the target
enum — which is how a typo or a stale name gets caught. A member that omits a
reference/slice column is left as the Go zero value (the property is omitted).

TypeScript references work both within an enum (self-references, e.g. `fanout`)
and across files. A **cross-file** reference emits an `import` for the target
enum's symbols and resolves values to its container (`StatIds.FishingSpeed`). The
import module is, in order of precedence:

1. the target enum's `tsModule`, if set — a verbatim specifier, e.g.
   `"@/model/stats"` (use this when the target's TS symbols are hand-written or
   behind a path alias, or it emits no TS of its own); otherwise
2. the path of the target's `tsOut` **relative to** this file's `tsOut`, with the
   extension stripped (`import { StatIds, type StatId } from "../stats.gen"`).

If the target has neither `tsOut` nor `tsModule`, the reference errors — there is
nothing to import. Only the container is imported as a value (and only when a
value actually uses it); the type is imported as `type`.

**Struct columns.** A column can be typed as an existing Go struct in the target
package (e.g. `derived: "[]EffectDsl"`). The generator loads that package with
`go/packages`, resolves the struct's real fields, and renders nested composite
literals against them — YAML keys map to fields by `json` tag (then field name),
and each value is rendered with the field's actual type (so `args: [10]` on an
`Args []float64` field emits `[]float64{10}`, not `[]int{...}`). Basic fields,
slices, pointers, and nested same-package structs are supported; a field from
another package is rejected (its import isn't wired up). Struct columns are
Go-only — a struct column in a `tsOut` or `ktOut` enum errors. Package types are
loaded lazily, so enum-only generation never invokes `go/packages`.

### Extra iterators (`iterators`)

List metadata columns to expose as extra iterators over their values, saving a
hand-written side file:

```yaml
iterators: [title, alias]
```

For each column this emits a Go container method `Iter<Field>() iter.Seq[T]`, a
TypeScript generator `iter<Field>()`, and a Kotlin `Sequence<T>` function. Each
yields the column in declaration order. Optional TypeScript columns yield
`... | undefined`; optional Kotlin columns yield nullable values. An
`iterators` entry that names a non-column errors.

## Generated Go API

For `type SlotName byte` the generator produces:

- Constants: `SlotNameMainWeapon`, … (including sentinels).
- `SlotNames` — a container struct value; field per member for typed access.
- `SlotNameInfo` — a struct embedding the enum plus `Name` and every metadata
  column.
- Container methods: `Values()`, `Infos()`, `Len()`, `All()` (an `iter.Seq`),
  `FromWire(byte)`, `Parse(string)`.
- Value methods: `Info()`, `TryGetInfo()`, `Valid()`, `Wire()`, `Name()`,
  `String()`, plus an accessor per metadata column (e.g. `Title()`, `Desc()`).
- With `marshalText: true`: `MarshalText`/`UnmarshalText`.

## Generated TypeScript

When `tsOut` is set, a parallel module is written with a `const` values object,
a derived union type (`type SlotName = (typeof SlotNames)[keyof typeof
SlotNames]`), an `Info` interface, and `Infos`/`Values`/`ByName` tables.
Sentinels are emitted as standalone consts and excluded from the union.

**The TS output directory is never created.** `tsOut` commonly points into a
separate repo (e.g. a viewer alongside this one), so the generator writes the
file only when its directory already exists; otherwise it prints a skip warning
and moves on. Go generation is unaffected. This keeps a consumer who uses this
package *without* that repo from having a stray external directory tree created —
they just get the Go enums. (Create the directory, or drop `tsOut`, to change
that.)

A metadata column that some members omit becomes an optional interface property
(`unit?: string`) and is left off those members' entries entirely, rather than
being filled with an empty string / empty array as the Go zero value requires.
Columns present on every member stay required.

`ByName` is keyed by the exact member name (unlike the Go side, whose unexported
lookup map is lowercased to back a case-insensitive `Parse`; TypeScript has no
lookup-time lowercasing). For the same case-insensitive resolution, a
`parse<Type>(name)` function is generated that trims and lowercases before
looking up, returning `undefined` for an unknown name.

## Generated Kotlin

When `ktOut` is set, the generator writes an `@JvmInline value class`. Members
live on its companion object, giving compact usage such as
`SlotName.MainWeapon` while retaining only the configured wire value at
runtime. The companion exposes `entries`, `byName`, `fromWire`, and
case-insensitive `parse`; metadata is emitted as a typed data class and map.

Go primitive types map to Kotlin primitives, slices become read-only `List<T>`,
and omitted metadata becomes nullable. Cross-enum fields use the referenced
value class and add an import when its `ktPackage` differs. Kotlin keywords are
escaped when a configured case mode produces one.

Aliases and sentinels are top-level typed values. Neither participates in
`entries`, lookups, or metadata.

Like TypeScript output, the Kotlin output directory is never created. A missing
directory produces a skip warning without affecting Go or other configured
outputs.

## Layout

| file          | responsibility                                        |
| ------------- | ----------------------------------------------------- |
| `main.go`     | flags, spec collection, output-path resolution        |
| `spec.go`     | spec parsing, type inference, underlying classification |
| `gen_go.go`   | Go renderer                                            |
| `gen_ts.go`   | TypeScript renderer                                    |
| `gen_kotlin.go` | Kotlin renderer                                        |
| `naming.go`   | identifier case helpers                                |

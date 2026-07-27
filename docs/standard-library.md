# Standard library

Import standard packages with `std:name`:

```go
import "std:mathutil"

func main() {
    print(mathutil.Clamp(12.0, 0.0, 10.0))
}
```

## Package guide

| Package | Main API |
| --- | --- |
| `std:mathutil` | `Sqrt`, `Pow`, `Floor`, `Ceil`, `Fabs`, `Abs`, `Min`, `Max`, `Clamp`, `Normalize2D` |
| `std:strings` | Search, trim, split, case conversion, and number formatting |
| `std:slices` | Generic `Contains`, `IndexOf`, `Reverse`, `Map`, `Filter`, `Reduce` |
| `std:collections` | Generational `SlotMap[T]` handles |
| `std:vectors` | `Vector2` and `Vector3` arithmetic |
| `std:rect` | Rectangle queries and set operations |
| `std:rand` | Seeded integer, float, range, and boolean random values |
| `std:time` | Monotonic time and duration formatting |
| `std:scheduler` | Cooperative coroutine scheduling |
| `std:log` | Formatted logging with `{}` placeholders |
| `std:test` | Assertions used by `llvmc -test` |

## `std:strings`

The package includes `Contains`, `IndexOf`, `HasPrefix`, `HasSuffix`,
`TrimSpace`, `Split`, `ToUpper`, and `ToLower`.

Number formatting uses `IntToString`, `Int64ToString`, `UInt64ToString`,
`F64ToString`, and `F64ToStringPrecision`.

## `std:collections`

`SlotMap[T]` stores values behind handles that become invalid after removal:

```go
import "std:collections"

items := collections.NewSlotMap[string]()
handle := items.Insert("hello")

if items.Valid(handle) {
    value := items.Get(handle)
    pointer := items.GetPtr(handle)
}
items.Remove(handle)
```

`Get` and `GetPtr` expect a valid handle, so call `Valid` when a handle may
be stale. The package also provides `Len`, `Clear`, plus `Values(&items)` and
`Handles(&items)` generators.

## `std:vectors` and `std:rect`

Vectors provide arithmetic plus `Length`, `LengthSquared`, `Normalize`,
`Dot`, `Distance`, and `Lerp`. `Vector3` also provides `Cross`.

Rectangles provide `Min`, `Max`, `Center`, `Contains`, `Intersects`,
`Intersection`, and `Union`.

## `std:rand`

Call `Seed` for repeatable output. `IntRange(min, max)` includes both bounds;
`FloatRange(min, max)` does not include its upper bound.

## Other packages

- `std:slices` provides `Contains`, `IndexOf`, in-place `Reverse`, `Map`,
  `Filter`, and `Reduce`.
- `std:time` provides `Now`, `ElapsedSeconds`, and `FormattedDuration`.
- `std:scheduler` provides `Entry`, `Scheduler.Schedule`,
  `ScheduleDelayed`, `HasPending`, and `Tick`.
- `std:log` provides `Format`, `Log`, `Info`, `Warn`, and `Error`. Formatting
  uses `{}` placeholders with `...Any` arguments.
- `std:test` provides `Assert`, `AssertFalse`, `AssertEqual`,
  `AssertNotEqual`, `AssertNil`, `AssertNotNil`, `AssertSliceEqual`, and
  `AssertApprox`.

See [the examples](examples.md#standard-library) for small runnable programs.
Exact signatures live in [`LANGUAGE.md`](../LANGUAGE.md#standard-library).

[Previous: Packages and imports](packages.md) ·
[Next: Testing](testing.md)

# `match`

`match` handles enum variants, a small set of ordinary values, and the type
inside an `Any`.

## Matching enums

Each arm names one variant. Payload names are available only inside that arm:

```go
match message {
    Message.Quit => {
        print("quit")
    }
    Message.Move(x, y) => {
        print(x)
        print(y)
    }
    Message.Text{body: text, urgent: isUrgent} => {
        print(text)
    }
}
```

Cover every variant, or add `_` as the final fallback:

```go
match message {
    Message.Quit => {
        print("quit")
    }
    _ => {
        print("something else")
    }
}
```

One arm cannot combine several variants.

## Matching values

Signed integers, booleans, and strings can be matched by value. An arm may
list several values, and the patterns may be expressions. These matches
always need a final `_` arm:

```go
match code {
    200, 201, 204 => {
        print("ok")
    }
    404 => {
        print("not found")
    }
    _ => {
        print("other")
    }
}
```

## Matching types

When the subject is an `Any`, each arm names a type. Write `name Type` to
bind the value at that type, or just `Type` to match and ignore it. These
matches always need a final `_` arm:

```go
func describe(a Any) string {
    match a {
        v int => {
            return "int"
        }
        v string => {
            return v
        }
        Point => {
            return "a point"
        }
        _ => {
            return "something else"
        }
    }
}
```

Inside the arm, the binding has the named type - `v` above is a real `int`,
not an `Any`. Any type works: a primitive, struct, enum, generic
instantiation (`Pair[int, string]`), pointer, map, or array.

An arm may name only one type, and no two arms may name the same type
(`int` and `i32` are the same type). An enum arm matches whichever variant
the value holds; match the bound value again to destructure it. A pointer
arm matches *any* pointer, whatever it points to - see
[Current limitations](current-limitations.md).

For a runnable program, see [examples/type_match](../examples/type_match/type_match.llx).

## Match expressions

Use `match` where a value is expected:

```go
label := match status {
    Status.Ready => "ready"
    Status.Failed(reason) => {
        print(reason)
        yield "failed"
    }
}
```

Every arm must produce a compatible value. A bare-expression arm produces
that expression. Every reachable path through a block arm must use
`yield value`; `return` still exits the enclosing function.

[Previous: Enums](enums.md) ·
[Next: Ownership and move](ownership.md)

# `match`

`match` handles enum variants and a small set of ordinary values.

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

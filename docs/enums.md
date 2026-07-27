# Enums

An enum variant may carry no data, positional data, or named fields:

```go
enum Message {
    Quit,
    Move(i32, i32),
    Text {
        body string
        urgent bool
    },
}

quit := Message.Quit
move := Message.Move(4, 7)
text := Message.Text{body: "hello", urgent: true}
```

Enums may also have methods:

```go
enum Status {
    Ready,
    Failed(string),
}

func (Status) IsReady() bool {
    return match this {
        Status.Ready => true
        _ => false
    }
}
```

Use [`match`](match.md) to inspect the active variant and bind its payload.

## Recursive enums

Direct recursive payloads would have infinite size, so use a pointer:

```go
enum Node {
    End,
    Next(*Node),
}
```

An enum can be compared only when every payload type can be compared. It can
be printed only when every payload type can be printed.

Like structs, enums may declare one `destructor()` method. That makes the
enum non-copyable; see [ownership and `move`](ownership.md).

[Previous: Structs](structs.md) ·
[Next: match](match.md)

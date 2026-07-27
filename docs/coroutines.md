# Coroutines

An `async` function runs until its first `await`, then returns a handle:

```go
async func Work() int {
    print("start")
    await
    print("finish")
    return 42
}

job := Work()
for !done(job) {
    resume(job)
}

if done(job) {
    print(result(job))
}

delete job
```

Calling `Work()` prints `start` immediately. Each `resume(job)` continues to
the next `await` or completion and returns whether more work remains.

`done(job)` reports completion. `result(job)` returns the declared result
after completion; before completion or after deletion it returns the type's
zero value.

## Handles and cleanup

A handle is non-copyable but may be stored in locals, struct fields, and
fixed arrays. Transfer ownership with [`move`](ownership.md).

`delete` releases the coroutine frame. It is safe on `nil`, and deleting a
bare handle local clears it.

Assigning a handle to the built-in `coroutine` type erases its result type,
so `result(handle)` is then unavailable.

## Current shape

Async functions are top-level functions, not methods or lambdas. Their
parameters and result must be copyable. They cannot directly `await` another
coroutine; call it and manage its handle instead.

Coroutines require optimized compilation and do not work with `-no-opt`.
For scheduling, use [`std:scheduler`](standard-library.md).

Try [`coroutines.llx`](../examples/coroutines/coroutines.llx) and
[`scheduler_demo.llx`](../examples/scheduler_demo/scheduler_demo.llx).

[Previous: Generators](generators.md) ·
[Next: Any and reflection](any-and-reflection.md)

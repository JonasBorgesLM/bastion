# ADR-0007: No dedicated timeout helper — FR-07 is satisfied by documenting the pattern

## Status
Accepted

## Context

FR-07 asks for "a per-operation timeout, integrated with `context.WithTimeout`."
The literal mechanism already exists in the standard library:

```go
ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
defer cancel()
result, err := bastion.Execute(ctx, breaker, op)
```

The open question, named explicitly when this was deferred out of B1, is
whether wrapping that pattern in a bastion helper earns its place, or whether
three idiomatic lines of standard library are simply the answer — a
`WithTimeout`, generic over the result type, taking a duration and an
operation and returning what the operation returns.

A wrapper justifies itself by removing a mistake callers actually make. The
one mistake this specific pattern invites is well known: constructing a
`context.WithTimeout` and forgetting to call the returned `cancel`, which
leaks the timer until the parent context is itself cancelled or garbage
collected.

## Decision

No helper. `go vet`'s standard `lostcancel` analyzer already detects exactly
that one mistake — a `context.WithTimeout` or `context.WithCancel` whose
`cancel` function is not called on every path — at compile-check time, with no
additional dependency: every consumer of this library already runs `go vet`
as part of an ordinary build, and this repository's own CI does too (the
`build & test` job). A wrapper cannot catch a mistake more cheaply than a
static analysis pass that already runs before the code ships, and a wrapper
that exists only to save three lines nobody was going to get wrong past `go
vet` is exactly the "wrapper that adds a name and nothing else" the general
engineering rules warn against.

FR-07 is satisfied by the pattern above, documented on [`Execute`](../../breaker.go)'s
own godoc and demonstrated in the package's runnable example once one exists
(B7). It needs no function of its own.

### Interaction with retry

A per-attempt timeout — bounding each individual attempt inside a
[`RetryPolicy`](../../retry.go) loop, rather than the call as a whole — is
already expressible with zero additional code, because `Retry` passes its
`ctx` straight through to `op` on every attempt:

```go
result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (T, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
	defer cancel()
	return bastion.Execute(attemptCtx, breaker, realOp)
})
```

This is the same reasoning ADR-0005 already relies on: `op` deriving its own
child context is not something bastion needs to know about or provide a
mechanism for — the standard library already gives `op` everything it needs
to bound itself, per attempt, per call, or not at all.

## The alternative that was rejected

**A generic `WithTimeout` wrapper equivalent to the three-line pattern above.**
Rejected on the `go vet` argument stated above — it does not
close a gap `go vet` leaves open. A version of the idea that *would* have
been worth taking seriously is a wrapper that also classifies the resulting
`context.DeadlineExceeded` in some special way relative to the breaker — but
ADR-0005 already settled how that classification works (by reading the outer
`ctx`, regardless of which context deadline actually fired), so there is
nothing left for a timeout-specific wrapper to add on that front either.

## Consequences

- `retry.go` and `breaker.go` carry the pattern in their own godoc rather than
  in a function signature, so a reader who greps for "timeout" in this
  package finds documentation, not a partially-redundant helper.
- If a real mistake around this pattern surfaces later — something `go vet`
  does not catch — that is new evidence and reopens this decision. Today,
  none has.

## Reopening criterion

A concrete mistake with this pattern is observed in practice that `go vet`'s
`lostcancel` analyzer does not already catch.

# ADR-0008: Fallback is a post-Execute, call-site function — never nested inside op

## Status
Accepted

## Context

FR-08 asks for "an optional fallback, executed when the circuit is open or
the call fails after retries." The type problem is already on the record at
the end of `options.go`: a fallback produces the call's return value, so it
is typed `T`, and configuring it on `Breaker` would force either a
`Breaker[T]` — which defeats one named breaker guarding a dependency called
from several call sites with different return types (FR-10) — or an
`interface{}` round trip, which is exactly what generics exist to avoid.
`options.go`'s own note already concludes the fallback has to be an argument
at the call site, where its type is known; this ADR is what that means
concretely, and the two questions the tracking issue named explicitly:
whether it runs on rejection only or also on a genuine failure after
retries, and how it is wired without corrupting the breaker's own
accounting.

**Where a fallback must not live: inside `op`.** The tempting shape is a
decorator around the operation itself —
`Execute(ctx, b, withFallback(fb, realOp))` — mirroring how `Retry` composes.
This is wrong for a reason specific to fallback and not to retry: if the
fallback ran *inside* `op` and succeeded, `Execute` would see a `nil` error
and count the call as a **success against the breaker**, even though the
real dependency never answered — the fallback did. The breaker's own health
signal would then be lying about the dependency it exists to protect,
silently, every time the fallback covers for it. This is a materially
different failure mode from anything ADR-0006 considered, and it rules out
composing fallback the way retry composes.

## Decision

`Fallback` is a plain generic function, called *after* `Execute` has already
returned and already updated every counter it is going to update:

```go
func Fallback[T any](
	ctx context.Context,
	result T,
	err error,
	fb func(context.Context, error) (T, error),
) (T, error)
```

```go
result, err := bastion.Execute(ctx, breaker, realOp)
result, err = bastion.Fallback(ctx, result, err, myFallback)
```

`Fallback` does two things and nothing else:

1. If `err` is `nil`, it returns `result, nil` unchanged — `fb` is never
   called on a successful outcome.
2. If `err` is non-nil, it calls `fb(ctx, err)` and returns whatever `fb`
   returns — **unless `ctx.Err() != nil`**, in which case it returns
   `result, err` unchanged, exactly as if no fallback had been configured.

That second exception answers the tracking issue's own subtlety, by
extension of ADR-0005's reasoning rather than by inventing a new rule: a
caller who cancelled `ctx` has already walked away, and calling `fb` on
their behalf — potentially doing real work (a cache read, a secondary
call) — is doing something nobody asked for anymore. `Fallback` declines to
do it, the same way `Execute` and `Retry` both decline to do unrequested
work once `ctx` is Done.

**On the tracking issue's question — rejection only, or also failure after
retries — the answer is: both, uniformly, because `Fallback` cannot tell
them apart and does not need to.** `err` is `ErrOpenState`,
`ErrTooManyRequests`, or the operation's own error surfaced by `Execute`
directly, or the last error out of a `Retry` loop that exhausted its
budget — `Fallback` treats all of them identically: a non-nil `err` that the
caller did not cancel out of. This matches FR-08's own wording ("circuit is
open **or** failure after retries") more literally than a design that had to
special-case which of the two happened.

## The alternative that was rejected

**A decorator wrapping `op`, run inside `Execute`.** Rejected above: it
lets the fallback's own outcome corrupt the breaker's accounting of the real
dependency's health, which is a correctness defect, not a style preference.

**Distinguishing rejection (`ErrOpenState`/`ErrTooManyRequests`) from a
genuine operation failure, and only falling back on the former.** Considered
because it is closer to the FR-08 wording's first clause read in isolation
("when the circuit is open"), but rejected because the second clause
("or the call fails after retries") already asks for exactly the case this
would exclude, and because it would force `Fallback` to know two specific
sentinel values and treat every other error differently — more surface, for
a distinction the fallback's own purpose (produce *something usable* when
the primary path did not) does not actually care about.

## Consequences

- A fallback that itself fails does not, and structurally cannot, corrupt
  the breaker's counters — `Fallback` never holds a reference to a
  `*Breaker` and never calls `Execute`. This is true by construction, not by
  a check; the test suite asserts it as a property (breaker state is
  identical whether the fallback succeeds, fails, or is never configured)
  rather than merely by absence of a code path.
- A caller who wants the fallback to run only on `ErrOpenState` specifically
  can still write that themselves with one `errors.Is` check before calling
  `Fallback` — nothing here prevents it, it is simply not the default `Fallback`
  itself applies.
- `fallback.go` is a new file, added to the package layout in
  REQUIREMENTS.md §6.1 — a fifth flat file alongside `retry.go`, not a method
  on `Breaker` and not a new `Option`.

## Reopening criterion

Not deferred. This is the decision B5 was blocked on.

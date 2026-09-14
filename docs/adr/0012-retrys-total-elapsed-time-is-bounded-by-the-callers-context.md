# ADR-0012: Retry's total elapsed time is bounded by the caller's context, not a MaxElapsedTime field

## Status
Accepted

## Context

`RetryPolicy` bounds attempts (`MaxAttempts`) and the delay between them
(`BaseDelay`, `MaxDelay`), but nothing in the type bounds the *total* wall-clock
time a call to `Retry` can spend. The worst case is `MaxAttempts` times each
attempt's own duration, plus the full backoff schedule between them — a number
no caller computes correctly in their head, and one that grows silently if a
dependency starts responding slowly rather than failing fast (issue #49).

`context.WithTimeout` around the whole `Retry` call is the standard-library
answer, and ADR-0007 already established the general principle: bastion does
not wrap a pattern the standard library and `go vet`'s `lostcancel` analyzer
already cover well. The question this ADR actually has to settle is narrower
and was raised explicitly rather than assumed away: a context deadline is
usually described as "aborting the in-flight attempt," while an elapsed-time
budget field on a retry policy is usually expected to mean "do not start
another attempt past this point, but let the current one finish." If
`context.WithTimeout` only delivered the first behavior, it would not actually
satisfy what a `MaxElapsedTime` field is normally asked for, and the field
would earn its place after all.

**It does not only deliver the first behavior, and this is provable from how
Go contexts work, not merely argued.** A context deadline is cooperative, not
preemptive: `ctx.Done()` closing does not stop a running goroutine or unwind a
call already in progress. `op(ctx)` — whatever it does — keeps running until
it returns on its own, checks `ctx.Err()` itself, or is otherwise given up on
by *its own* logic. `Retry`'s loop never forcibly aborts an in-flight attempt;
it never could, with or without a context deadline.

What a context deadline *does* stop, in `Retry`'s loop as it already stands, is
the *next* attempt. `sleepRespectingContext` is called between every failed,
retriable attempt and the next one — including the zero-delay case, which
still performs a non-blocking `ctx.Done()` check rather than skipping straight
to the next attempt — so a context that is Done, however it got that way,
prevents any further attempt from starting. This is exactly the semantics
being asked for, already implemented, and already covered by
`TestRetry_AlreadyCancelledContextStopsBeforeTheNextWait` (an already-cancelled
context lets the first attempt run and stops before the second) and
`TestRetry_ContextCancelledDuringTheWaitReturnsAtOnceWithCtxsOwnError` (a
context cancelled mid-wait returns at once rather than finishing the wait).
Neither test was written for this ADR — both predate it, verifying FR-05's
cancellation accounting — but they are the same mechanism this ADR is about,
observed from a different angle.

## Decision

No `MaxElapsedTime` field. A caller who wants a total elapsed-time budget
wraps the whole `Retry` call the same way ADR-0007 already documents wrapping
`Execute`:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (T, error) {
	return bastion.Execute(ctx, breaker, realOp)
})
```

This bounds total time the way the issue asked for: no attempt starts once the
deadline has passed, and an attempt already in flight when the deadline passes
is not forcibly cut off — it is left to finish (or to notice its own `ctx` is
Done, if it derives a per-attempt sub-context per ADR-0007's other documented
pattern). `Retry`'s own godoc is updated to state this explicitly, alongside
the existing per-attempt-timeout pattern, so a reader looking for "how do I
bound the whole call" finds an answer next to "how do I bound one attempt"
rather than having to infer it from FR-05's cancellation accounting.

## The alternative that was rejected

**A `MaxElapsedTime time.Duration` field, checked the same place
`IsRetriable` is checked (ADR-0011): `if time.Since(start) > p.MaxElapsedTime
{ return zero, lastErr }`.** Rejected because it would duplicate a bound the
caller's own `ctx` already provides, would need its own `Clock` plumbing to be
testable without the wall clock (matching the discipline the rest of this
package holds itself to — NFR-05), and — the deciding point — could produce
behavior *inconsistent* with `ctx`'s deadline rather than merely redundant
with it: a caller who sets both a `context.WithTimeout` and a
`MaxElapsedTime` with different durations would have two competing bounds and
no principled way to know which one bastion would honor first, for no
capability neither one already provides alone.

## Consequences

- `RetryPolicy`'s shape is unchanged by this ADR; only its godoc and `Retry`'s
  gain the documented pattern.
- A caller who genuinely wants "abort the in-flight attempt too, not just stop
  starting new ones" gets that already, for free, if their `op` respects
  `ctx` — which is the same requirement ADR-0007's per-attempt pattern already
  states. Nothing new is asked of callers.

## Reopening criterion

A concrete case surfaces where `context.WithTimeout` around the whole `Retry`
call does not deliver "no further attempt starts past this point" — something
the existing tests referenced above do not already cover — or where a caller
has a real need to bound total elapsed time independently of `ctx`
(untrusted, caller-supplied policies with no access to construct their own
context, say). Today, none has.

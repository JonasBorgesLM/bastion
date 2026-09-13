# ADR-0006: Retry composes around the breaker, not inside it

## Status
Accepted

## Context

`Retry` and `Breaker` are separate, composable units — REQUIREMENTS.md already
decided that retry is not built into the breaker itself. What is still open is
the order a call site nests them in, and the two orders behave differently:

**Breaker wraps Retry** — `Execute(ctx, b, func(ctx) (T, error) { return
Retry(ctx, policy, realOp) })`. The whole retried sequence runs inside one
admitted call. The breaker sees one outcome — success, or the retry budget's
final failure — no matter how many real attempts happened underneath.

**Retry wraps Breaker** — `Retry(ctx, policy, func(ctx) (T, error) { return
Execute(ctx, b, realOp) })`. Each retry attempt is its own `Execute` call. The
breaker evaluates, and counts, every attempt individually.

Both are expressible today with no new code beyond `Retry` itself — the order
is just ordinary function nesting at the call site. The question is which one
the library recommends, and why.

## Decision

**Retry wraps the breaker.** The library recommends:

```go
result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (T, error) {
	return bastion.Execute(ctx, breaker, realOp)
})
```

Three reasons, in order of how much they'd cost to get wrong:

1. **The breaker's threshold keeps its literal meaning.** `WithFailureThreshold(n)`
   promises *n* consecutive failed calls to the dependency. With the breaker
   on the outside, one logical request whose retry loop fails three times
   before succeeding would, on its own, silently consume most or all of a
   low threshold — the breaker's own count would no longer describe failures
   of the *dependency*; it would describe failures of *one caller's retry
   loop*, amplified by however many attempts that loop allows. With the
   breaker on the inside, `n` real, distinct attempts against the dependency
   is what actually has to happen for the breaker to trip, regardless of how
   many logical requests those attempts came from.
2. **An already-open circuit stops the retry loop for free.** Once the
   breaker trips, every subsequent attempt inside a live retry loop gets
   `ErrOpenState` back immediately, without touching the network and without
   waiting out the backoff that would otherwise precede it. Retry needs no
   special knowledge of the breaker to benefit from this — it is a direct
   consequence of each attempt being its own `Execute` call. A caller who
   wants to stop retrying altogether the moment the circuit opens can check
   `errors.Is(err, bastion.ErrOpenState)` inside their own retry-eligibility
   logic, but even without doing that, no further real calls are attempted.
3. **A Half-Open probe stays a single, bounded probe.** With retry inside the
   breaker, a probe call could itself retry several times before reporting
   back, holding the one probe slot open for the whole inner retry sequence.
   With retry outside, each attempt is its own probe: the first attempt after
   the open timeout resolves the window by itself, one way or the other, and
   a failing probe reopens the circuit — which then fails the loop's
   remaining attempts fast, per point 2.

## The alternative that was rejected

**Breaker wraps Retry.** Rejected primarily on point 1 above: it makes
`FailureThreshold` an increasingly inaccurate description of what actually
has to go wrong before the circuit trips, silently scaled by whatever
`MaxAttempts` a caller happens to configure — two numbers that, on this
ordering, interact in a way neither one's own godoc mentions. It also removes
the breaker's ability to interrupt a retry sequence already in progress: from
the breaker's perspective the whole sequence is one opaque call, so it can
only judge it after the fact, never cut it short.

It is not a wrong design — a caller could still choose it deliberately, if
what they want is: "the breaker should only care whether a *logical request*
ultimately failed, not how many raw attempts that took." It is not what this
library defaults to recommending.

## Consequences

- **A low `FailureThreshold` combined with a multi-attempt `RetryPolicy` can
  trip the circuit before a single logical request's own retry budget is
  exhausted.** `FailureThreshold(2)` and `MaxAttempts(3)`: two failed real
  attempts open the circuit, and the third attempt — which might have
  succeeded — gets `ErrOpenState` instead of ever running. This is not a bug;
  it is the direct, intended consequence of point 1. Choosing
  `FailureThreshold` and `RetryPolicy.MaxAttempts` together, with this
  interaction in mind, is documented on `Retry`'s own godoc rather than left
  for a caller to discover from an incident.
- The composition test in `retry_test.go` asserts this exact ordering — which
  of the two sees how many calls — so a future change cannot silently
  contradict this ADR without a test going red first.

## Reopening criterion

Not deferred. If a real caller needs "the breaker judges the logical request,
not the raw attempt count," that is a legitimate, different use case — served
today by nesting the other way at that specific call site, not by changing
this library's recommendation.

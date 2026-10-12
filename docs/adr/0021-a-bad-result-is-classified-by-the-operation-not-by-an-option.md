# ADR-0021: A bad result is classified by the operation, not by an option

## Status
Accepted

## Context
`WithIsFailure` (FR-04) classifies the error an operation returns. It never
sees the operation's result, because `Breaker` is not generic: only `Execute[T]`
is. A dependency that reports failure through a value rather than an error
counts as a success on every call. The usual case is an HTTP client returning a
`503` response with a nil error.

That is not hypothetical. usher's gateway wraps its reverse proxy's
`RoundTrip` in `Execute`, and an upstream answering `503` to every request never
opened the circuit (usher#118), the most common way a dependency degrades.
usher/ADR-0016 had rejected a hand-rolled breaker because "what counts as a
failure … is what bastion exists to get right", and bastion could not see it.
Reported as #85.

## Decision
Classifying a result is the operation's job, and the pattern is documented:
the operation returns an error carrying the result it considers unhealthy, and
the caller unwraps it after `Execute` if it still wants the result.
`WithIsFailure`'s documentation says a result is invisible to it and points at
`ExampleExecute_resultAsFailure`, which runs as a test. It shows the breaker
opening on `503` results while each caller still receives the `503`.

No API is added.

## The alternative that was rejected
**A per-call option on `Execute`, `WithResultClassifier[T]`, taking a
`func(T, error) bool`.** It would make the case direct: no wrapper error, no unwrap. It was
rejected for three reasons:

- **It is generic, and `CallOption` is not.** It would need either a second,
  generic option type, which means two option systems on one function, or a
  type assertion inside `Execute`, which turns a type mismatch into a runtime
  surprise instead of a compile error.
- **It composes awkwardly with `WithIsFailure`.** With both set, which wins,
  or do they OR? Any answer is a rule every caller must learn. The pattern
  needs no such rule: the operation decides, and the classifier sees the
  result as an ordinary error.
- **The pattern is about eight lines and already the shape usher arrived at
  independently.** The gap #85 described was discoverability, not capability:
  nothing in the documentation suggested the result was invisible. That is
  fixed where a reader looks: on `WithIsFailure` itself, and in a runnable
  example.

This follows ADR-0018's reasoning for metrics adapters. When a pattern is short
and the API cost is real, the library documents the pattern.

## Consequences
- Every caller writes the conversion. A mistake there, such as forgetting the
  unwrap, changes what the caller receives (an error instead of the response).
  The example makes the full shape copyable to avoid that.
- `Execute` stays one function with one option type, and `WithIsFailure` keeps
  one input.

## Reopening criterion
A second integration needing the same conversion and getting it wrong, such as
a lost result or a double count, would show that the pattern is not enough.

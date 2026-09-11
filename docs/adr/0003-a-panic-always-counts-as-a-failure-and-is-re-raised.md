# ADR-0003: A panic in the operation always counts as a failure, and is re-raised unchanged

## Status
Accepted

## Context

FR-11 settles that a panic must not leave `b.mu` held (a `defer` around the
recover, not a question of judgment) and must reach the caller unchanged. What
it does not settle, and what actually needs a decision, is whether a panic
**counts against the failure threshold** — and if it does, whether it goes
through the same classifier as a returned error (FR-04, `WithIsFailure`).

The case for treating it as *not* a failure: a panic is very often a bug in
the caller's own operation closure — a nil dereference, an index out of
range, a bad type assertion — and FR-04's entire premise is that
classification should reflect the remote dependency's health, not the
caller's code quality. Counting a caller bug against the breaker looks like
exactly the mistake FR-04 exists to prevent, at first glance.

The case against that reasoning: bastion has no way to tell a caller bug
apart from a panic *caused by* the remote dependency — a client library that
panics on a malformed or adversarial response is not hypothetical, and from
inside `Execute` the two are indistinguishable. Treating panics as free
(never counted) means an operation that panics on every call never opens its
circuit, and keeps being invoked at whatever rate the caller retries at,
forever. That is worse than the failure mode FR-11 is trying to prevent.

## Decision

A panic always counts as a failure. It does not go through `WithIsFailure`.

Mechanically: `Execute` recovers the panic, updates the failure counters and
fires `Hooks.OnCall` with `Counted: true` exactly as it would for any other
counted failure, releases the lock (already guaranteed by `defer`, FR-11),
and then re-panics with the original recovered value — not a wrapped or
re-typed one — so the caller's stack, their own `recover()` if they have one,
and any panic-reporting middleware they run see precisely what they would
have seen with no breaker in front of the call.

`WithIsFailure` is not consulted, because it is typed `func(error) bool` and a
recovered panic value is not an `error` — `recover()` returns `any`, and it is
routinely a string or a struct, not something implementing the `error`
interface. Coercing it into one just to run it through a classifier built for
answers-from-a-remote-service would be manufacturing an error that never
existed, for a classifier whose entire job (FR-04) is to read the *shape of a
response*. A panic is not a response.

## The alternative that was rejected

**Route a synthesized `error` through `WithIsFailure`, so a caller could
excuse specific panics the way they excuse a 404.** Rejected on two grounds.
First, it asks every classifier function in every breaker to additionally
handle a case it was never written to see, silently changing what
`WithIsFailure`'s contract means. Second, and more concretely: a classifier
that excuses a panic is a classifier that tells the breaker to treat "this
code just crashed" as not-a-failure, which is a strange thing to opt into on
purpose and an easy thing to opt into by accident if the synthesized error's
message happens to match an existing exclusion rule (a caller excusing
`"not found"` substrings, say, and a panic message that happens to contain
that text).

## Consequences

- An operation with a caller bug that panics unconditionally will open its
  circuit after `FailureThreshold` panics, exactly as it would after that
  many returned errors. This is arguably a feature: it turns "my handler
  panics on every request" into a fast, visible, breaker-shaped failure
  instead of a silent one, and `Hooks.OnStateChange` reports it the same way
  it reports any other trip.
- A half-open probe that panics reopens the circuit, per the ordinary FR-01
  half-open-failure transition — no special case needed, because a panic is
  just another counted failure by this point.
- `CallEvent.Err` for a panic-derived call needs a decision at implementation
  time: either it carries the recovered value re-wrapped as an `error` for
  the hook's benefit (since `Hooks.OnCall`'s `Err` field is typed `error`),
  or it carries `nil` with a separate signal. That is an implementation
  detail of B5's hook wiring, not an ADR — this decision only fixes that
  `Counted` is `true` either way.

## Reopening criterion

Not deferred. This is one of the two decisions (with ADR-0004) B1 was
specifically blocked on for FR-11/FR-12.

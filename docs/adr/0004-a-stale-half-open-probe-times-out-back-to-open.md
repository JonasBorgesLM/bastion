# ADR-0004: A stale half-open probe expires back to Open, on the same timeout

## Status
Accepted

## Context

`Execute` is synchronous: it blocks the calling goroutine until `op` returns.
Bastion spawns no goroutine and owns no timer (NFR-05, IR-03), so it cannot
reach into another goroutine and force a hung `op` to return — nothing in the
library can un-block that call. FR-12 is not asking for that; it is asking
that one hung call cannot leave the *circuit* stuck.

Concretely: with `WithHalfOpenMaxCalls(1)` (the default), a probe is admitted,
`halfOpenInFlight` becomes 1, and the calling goroutine blocks inside `op`. If
`op` never returns — a client library ignoring context cancellation, a
connection that never times out — every other goroutine calling `Execute`
sees the allowance exhausted and gets `ErrTooManyRequests` forever. That is
worse than `StateOpen`: an open circuit recovers on its own after
`openTimeout`; a circuit wedged in this state does not, because nothing ever
triggers the transition that would clear it.

A plain in-flight counter has no way to tell "still running, probably fine"
apart from "hung forever." The fix has to be time-based, using the one tool
the breaker already reads: the `Clock`.

## Decision

Recording the admission time of an in-flight half-open probe, and treating a
probe whose admission is older than `openTimeout` as expired:

- On admitting a probe, store `halfOpenAdmittedAt = clock.Now()` alongside the
  existing in-flight count, and stamp it with a generation counter,
  `halfOpenGeneration`, incremented on every Open → Half-Open entry.
- A call arriving while `StateHalfOpen` and the allowance is exhausted checks
  `clock.Now().Sub(halfOpenAdmittedAt)` against `openTimeout` before rejecting.
  If it has **not** elapsed, reject with `ErrTooManyRequests` as today. If it
  **has**, the outstanding probe is treated as failed by timeout: the circuit
  transitions back to `StateOpen` (the ordinary half-open-failure path, FR-01),
  which immediately makes it eligible for the ordinary lazy Open → Half-Open
  re-evaluation the ordinary state machine already performs. No new state, no
  new timer — this reuses the transition and the timeout both already have to
  exist for FR-01.
- The generation counter is what makes the eventually-returning hung call
  safe: when the original `op` finally does return (successfully or not), its
  completion callback compares the generation it was admitted under to the
  breaker's current generation. If they no longer match, the completion is
  discarded — it does not touch the counters, does not fire a transition, and
  does not fire `Hooks.OnStateChange` a second time. Without this, a probe
  that returns ten minutes late could silently close a circuit that has since
  reopened and failed again for an unrelated reason.

No new `Option`. The half-open lease reuses `WithOpenTimeout`'s value rather
than introducing a second duration to configure, because a second knob nobody
has asked for yet is exactly what REQUIREMENTS.md §6's minimal-surface
argument was written to prevent (see the fallback note at the end of
options.go for the same reasoning applied elsewhere).

**This is a safety net, not a substitute for a bounded caller.** The godoc on
`Execute` states, as a recommendation rather than an enforced rule, that an
operation invoked as a probe should itself respect `ctx`'s deadline where one
is set — this ADR's mechanism is what happens when that recommendation is not
followed, not a replacement for following it.

## The alternative that was rejected

**Require the caller to bound every probe with `context.WithTimeout`, and
document that an unbounded probe is a misuse of the library.** Correct in
principle and rejected as the *only* mechanism, because FR-12 explicitly asks
that one hung call not strand the circuit — an invisible caller obligation
that, if forgotten once, produces exactly the failure FR-12 names is not a
requirement met, it is a requirement documented and then not enforced. The
chosen design keeps the recommendation (a bounded probe recovers faster and
without ever risking two overlapping probes) while making the failure mode
survivable even when it is not followed.

## Consequences

- **Overlapping probes become possible.** If the original probe was merely
  slow rather than truly hung, and it returns after the lease has already
  expired and a second probe has been admitted, both are in flight briefly.
  `WithHalfOpenMaxCalls` bounds concurrent *admissions*, not concurrent
  *completions* of stale ones — this is the surprise traded for the
  obligation, named explicitly rather than discovered later.
- **A slow-but-healthy dependency can flap.** A probe that reliably takes
  longer than `openTimeout` to answer will have its lease expire, reopen the
  circuit, and then be re-admitted as a fresh probe on the next call,
  indefinitely — the circuit never stabilizes in Closed. This is a real cost
  of coupling the half-open lease to `openTimeout` rather than a separate,
  longer duration, and is the first thing to revisit if it shows up in
  practice (see Reopening criterion).
- Every half-open admission and every stale-completion discard is exactly the
  kind of state transition NFR-06 requires a test for, including the
  generation-mismatch case specifically — a test that admits a probe, expires
  its lease, admits a second one, and then delivers the first probe's
  (now-stale) result, asserting the breaker's state reflects only the second
  probe.

## Reopening criterion

Revisit coupling the half-open lease to `openTimeout` — and consider a
separate `WithHalfOpenTimeout` — if a real dependency's healthy response time
is close to or exceeds a reasonable `openTimeout`, causing the flapping
described above. Until that is observed, one duration is one fewer thing to
configure and get wrong.

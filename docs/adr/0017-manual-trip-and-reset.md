# ADR-0017: Manual Trip is sticky until Reset; Reset clears everything; both are visible as Manual

## Status
Accepted

## Context

A host has no way to open or close a circuit deliberately (issue #54). Two
real situations have no answer today: planned maintenance (a dependency is
going down in two minutes, on a schedule the operator knows and the breaker
does not), and a confirmed fix (the dependency was repaired seconds ago, but
`openTimeout` has not elapsed, so the circuit will not try it).

Three questions have to be answered on the record before writing the two
methods, because each has a wrong answer that looks reasonable:

## Question 1: does a manual trip expire on openTimeout?

**No — it is sticky until `Reset` is called.** The case for auto-expiry
sounds appealing (it reuses the machinery that already exists), but it fails
the exact scenario motivating this feature: `openTimeout` is tuned for fast
*automatic* recovery probing — seconds, typically — while a planned
maintenance window is commonly minutes. A manual trip that quietly expires
mid-maintenance re-admits a probe into a dependency the operator explicitly
said was still down, which is worse than not having the feature: it looks
like protection and silently is not.

**The alternative given real consideration: `Trip` takes a duration,
auto-reverting via the same lazy-evaluation mechanism `openTimeout` already
uses** (compare `now` against an anchor on the next call — no scheduling,
consistent with the "nothing sleeps, nothing schedules" invariant). Rejected
anyway, on two grounds. First, it needs a *second*, parallel timeout
mechanism living alongside `openTimeout` inside `effectiveState` and
`admit` — exactly the kind of state-machine-adjacent change CLAUDE.md
already singles out as warranting complex-task treatment regardless of diff
size, for a capability the host can already get by simpler means. Second:
a host that wants a time-bounded manual override already has everything it
needs by composing `Trip` now and `Reset` after its own maintenance window,
using its own scheduling — outside this library, exactly where "nothing
sleeps, nothing schedules" already says a timer belongs. Building a second
in-library timeout to save a caller one scheduled call is not a trade this
library's own stated priorities support.

The real risk auto-expiry was trying to solve — a forgotten override causing
an outage nobody can explain — is answered differently: see Question 3.

## Question 2: does Reset clear the failure count, or just the state?

**Both, plus the manual-trip flag.** `Reset` means "start genuinely fresh,"
not "flip the state back." An operator calling it because "the dependency is
fixed" wants a circuit that is not one ordinary failure away from re-
tripping — leaving a stale `ConsecutiveFailures` close to `FailureThreshold`
would make the very next real failure look like corroborating evidence for
a problem the operator just confirmed does not exist anymore. `Reset` clears
the failure count, clears any manual trip, and moves the circuit to
`StateClosed`, unconditionally, in one call.

## Question 3: does StateChangeEvent distinguish manual from automatic?

**Yes for the event, and — more usefully — yes for `Counts` too.** An
operator debugging from the event stream needs to know whether a transition
was evidence or a deliberate override, so `StateChangeEvent` gains
`Manual bool`, false on every existing (automatic) transition.

But the event stream is the wrong tool for the risk Question 1 deferred —
"forgotten override, outage nobody can explain" is a question about
*current, ongoing* state ("is this breaker sitting there manually open right
now"), not about a moment in the past a handler had to be listening for.
[`Counts`](0015-counts-answers-only-what-the-hook-stream-cannot.md) already
exists specifically to answer "what does this look like right now" without
requiring a host to have caught the right event — this is exactly that
shape of question, so `Counts` gains `Manual bool` too: pollable, by a health
check or a dashboard, at any time, entirely independent of whether a hook
handler happened to be wired up when the trip occurred. This is a
meaningfully stronger mitigation for the risk than the event alone would
have been, and it is the same reasoning ADR-0015 already used to draw
`Counts`'s boundary, applied here rather than invented fresh.

## Decision

```go
// Trip forces the circuit to StateOpen immediately, regardless of current
// state or accumulated evidence. Unlike an evidence-driven Open, a manual
// trip does not expire on WithOpenTimeout -- it rejects every call with
// ErrOpenState until Reset is called (ADR-0017). Fires Hooks.OnStateChange
// with Manual true if this call actually changes the circuit's raw state;
// calling Trip again while already Open re-affirms the trip (refreshing the
// episode) without firing a second event, the same "From and To are always
// different" rule every other transition already follows.
func (b *Breaker) Trip(ctx context.Context)

// Reset clears everything a manual override or accumulated evidence left
// behind: any manual trip, ConsecutiveFailures, and the circuit's state,
// unconditionally to StateClosed (ADR-0017). Fires Hooks.OnStateChange with
// Manual true if the circuit was not already Closed.
func (b *Breaker) Reset(ctx context.Context)
```

`StateChangeEvent` gains `Manual bool`. `Counts` gains `Manual bool`, true
only while `State` is `StateOpen` and that Open is a standing manual trip
(matching the same "zero/false unless the relevant State" convention
`OpenedAt` already established).

Internally: a new unexported `manualTrip bool` field. `effectiveState`
checks it first, before the existing `switch` — while true, it always
reports `StateOpen`, unconditionally, before `openedAt`/`openTimeout` are
even consulted. `Trip` and `Reset` mutate `b.state` directly (not through
`admit`), bump `halfOpenGeneration` and reset `halfOpenInFlight` exactly the
way `admit` already does on every transition — so a Half-Open probe already
in flight when either is called is correctly discarded as stale by
`complete`'s existing generation check (ADR-0004) rather than resolving a
window that no longer means what it did when the probe was admitted into
it.

## Consequences

- `Trip`/`Reset` take `b.mu` like every other mutating path, and fire hooks
  only after releasing it (FR-09, IR-02), matching `admit`/`complete`.
- No new exported error, no new invalid-input case: neither method can fail.
- A host that wants a time-bounded manual override composes `Trip` and
  `Reset` with its own scheduling; bastion still owns no timer of its own
  (NFR-05).

## Reopening criterion

A concrete report that composing `Trip`+`Reset` with a host's own scheduler
is not sufficient for a real time-bounded maintenance workflow — not a
theoretical preference for the library owning the timer instead. Today,
none has.

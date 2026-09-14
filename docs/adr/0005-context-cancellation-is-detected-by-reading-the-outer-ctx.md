# ADR-0005: Context cancellation is detected by reading the outer ctx, not by matching the returned error

## Status
Accepted

## Context

FR-05 requires that a context the caller cancelled counts as neither a success
nor a failure. Counting it as a failure lets a client-side deadline open a
circuit on a dependency that is answering fine; counting it as a success hides
a dependency that genuinely is not answering.

The hard part, named explicitly in the issue that tracked this ADR, is telling
two events apart that can produce the same-looking error:

1. The caller's own context — the one passed into [`Execute`] as `ctx`, and
   handed to `op` unchanged — was cancelled or hit its deadline while `op` was
   running. This is the case FR-05 is about.
2. `op` derived its **own** context internally (`context.WithTimeout(ctx,
   shorterDuration)`, or an entirely separate one for an internal retry), and
   *that* context's deadline elapsed. `op` may well return
   `context.DeadlineExceeded` here too — but the caller did not cancel
   anything; the dependency (or `op`'s own internal policy) is the one that
   timed out. This is an ordinary failure and must count against the
   threshold like any other.

`errors.Is(err, context.Canceled)` or `errors.Is(err, context.DeadlineExceeded)`
cannot distinguish these. Both cases can legitimately produce either sentinel,
and `op` might not even return one — a database driver that wraps
`context.DeadlineExceeded` in its own error type produces the same ambiguity
from the other direction.

## Decision

After `op` returns, `Execute` reads `ctx.Err()` — the `Err()` method of the
exact `context.Context` value that was passed into `Execute` and forwarded to
`op` — not any context `op` may have derived from it internally.

If `ctx.Err() != nil`, the call is accounted as cancelled: it moves neither
the Closed-state consecutive-failure counter nor (if it was a Half-Open probe)
resolves the probe's window one way or the other. `Hooks.OnCall`'s
`CallEvent.Counted` is `false`. `op`'s own return value and error still reach
the caller completely unchanged, exactly as for every other outcome (ADR-0001)
— cancellation accounting is invisible to the caller; it only changes what the
breaker does with the result internally.

This works because `ctx.Err()` reports only whether *that specific context
value* is Done — a child context derived from it via `context.WithTimeout` or
`context.WithCancel` can expire on its own without ever marking its parent
Done. Reading the outer `ctx` is therefore immune to exactly the ambiguity in
the Context section: case 2's internally-derived context times out without
`ctx.Err()` ever becoming non-nil, so it is correctly classified as an
ordinary failure through the normal path; only case 1 — the context actually
given to `Execute` — trips the cancellation accounting.

`classify` (the FR-04 error-taxonomy hook) is not consulted at all when
`ctx.Err() != nil`. A caller's `WithIsFailure` is answering "does this error
describe an unhealthy dependency"; a cancelled context describes nothing about
the dependency, so routing it through the same function would ask the
classifier a question it was never given the information to answer.

### The default classifier, closed at the same time

With cancellation handled as a gate ahead of classification, the *default*
`WithIsFailure` behavior — used when a caller supplies none — no longer has an
open question attached to it: every non-`nil` error counts (already the
`classify` fallback in `breaker.go`, in place since B1 as a documented
placeholder pending this ADR). It is now the settled default, not a
placeholder. `WithIsFailure`'s godoc is updated to state it plainly rather than
say a default does not exist yet.

## The alternative that was rejected

**Match the returned error against `context.Canceled` /
`context.DeadlineExceeded` with `errors.Is`.** This is what most libraries in
this space reach for first, and it is exactly the approach the Context section
shows cannot work: it is fooled in both directions. A wrapped
`context.DeadlineExceeded` from `op`'s own internal timeout reads as caller
cancellation and wrongly escapes the failure count (hiding a dependency that
is genuinely not answering — the second failure mode FR-05 names). A
dependency that legitimately returns its own unrelated
`context.DeadlineExceeded`-flavored error (some client libraries do, for
reasons that have nothing to do with the caller's context) would be
misclassified the same way. Reading `ctx.Err()` sidesteps error-matching
entirely by asking the one value that actually knows whether the *caller*
cancelled: the context object itself.

## Consequences

- **A call that raced to completion right as its context was cancelled is
  discarded from accounting even if it actually succeeded or failed
  meaningfully.** `op` returning cleanly a few nanoseconds before `ctx.Err()`
  would have flipped to non-nil, versus a few nanoseconds after, is not
  something `Execute` can or should try to resolve more precisely — the
  caller stopped waiting on this call, so what the breaker does with its
  outcome internally is a judgment call, and FR-05 already settled it as
  "discard" rather than "still count it."
- **A Half-Open probe that gets cancelled frees its slot but does not resolve
  the window.** `halfOpenInFlight` is decremented so the slot is available
  again, but the window's generation is *not* bumped — an outstanding sibling
  probe (under `WithHalfOpenMaxCalls > 1`) can still resolve it normally, and
  if none does, ADR-0004's staleness lease eventually reopens it. This
  composes with ADR-0004 rather than needing new machinery.
- **A caller who cancels every call before it finishes gets a circuit that
  never leaves Half-Open on its own.** If every probe into a window is
  cancelled before it resolves, `halfOpenInFlight` keeps returning to zero
  without the window ever being decided, and — because it is never at
  capacity for a full `openTimeout` — ADR-0004's staleness fallback does not
  trigger either. This is a real, accepted gap: bastion genuinely cannot know
  whether the dependency is healthy when it never sees a call finish. It is
  not a defect to route around with more machinery; it is the honest
  consequence of FR-05 refusing to guess.
- `errors.go`'s `TODO(B2)` note and `breaker.go`'s `classify` `TODO(B2)`
  comment are both resolved by this ADR; see the commit implementing it for
  where each was removed or rewritten.

## Reopening criterion

Not deferred. This is the decision that was blocking B2.

# ADR-0010: A hook panic never corrupts bookkeeping; op never running is accounted like a cancelled call, not a failure

## Status
Accepted

## Context

`Execute` calls hooks with no `recover` around them (issue #46, found by
audit, reproduced against the published v0.1.0). The specific failure: an
`OnStateChange` handler that panics on the Closed→Half-Open transition —
after `admit` has already incremented `halfOpenInFlight` for this call —
aborts `Execute` before `op` ever runs and before `complete` ever runs to
release that slot. Every subsequent call, from every other caller of the
same breaker, gets `ErrTooManyRequests` until ADR-0004's staleness lease
expires. A bug in observability code becomes an outage for a dependency that
was never actually unhealthy.

Fixing "don't leak the slot" is one line. Deciding what a hook panic *means*
is not, and three questions have to be answered together or the fix just
moves the bug:

1. **Does op still run if the admission-stage hook panics first?** Letting
   it run preserves the guarded call regardless of hook health, which sounds
   attractive — but it means Execute continuing past an admission it cannot
   fully explain, and it complicates every downstream question about what
   "the call's outcome" means when part of the call's own bookkeeping is
   already broken. Not letting it run is simpler and is what this ADR
   decides: op does not run this time; the caller finds out why, because the
   hook's panic reaches them (point 3).
2. **What does the breaker conclude about the dependency when op never
   ran at all?** This is the question with no free answer. `outcomeFailure`
   is available and is what FR-11/ADR-0003 already uses for op's own panic —
   but reusing it here would mean a bug in the *host's* hook code trips the
   circuit against a dependency that did nothing. `outcomeCancelled` is
   available too, and its actual meaning — "the breaker has no evidence
   either way" — is a more honest description of this situation than
   "failure" is.
3. **What happens to the hook's own panic?** Swallowing it hides a real bug
   in the host's code forever, since bastion has no other channel (it never
   logs, IR-04). Re-raising it, after bookkeeping is settled, puts it exactly
   where an `op` panic already goes — visible to whatever the host's own
   panic handling does.

`runtime.Goexit` sharpens the same three questions rather than adding new
ones, with one hard constraint: nothing a library does can make a goroutine
"resume" past a `Goexit` call. Deferred functions run; the code that would
otherwise execute after the `Goexit`-ing call does not. That applies equally
whether the `Goexit` originates in a hook or in `op` itself (a caller
invoking `t.Fatal` inside an operation closure, most realistically). What a
library *can* still guarantee is that its own shared state — the
`halfOpenInFlight` counter this whole issue is about — is resolved before
the goroutine finishes dying, by registering the resolution as a `defer`
ahead of anything that might not return normally.

## Decision

**Bookkeeping resolution is registered as a single `defer`, ahead of both
the admission-stage hook call and the call to `op`.** That `defer` runs on
every path out of that scope: a normal return, a panic anywhere inside it,
or a `Goexit` anywhere inside it. It is the only place `complete` is called
for an admitted call.

**Every hook call site is wrapped with a recovering helper**
(`runHookSafely`), so a panic in one hook can never prevent bastion's own
completion logic, or a sibling hook, from running.

**Classification, precisely:**

| What happened | Outcome | Why |
| --- | --- | --- |
| `op` never started (the admission hook panicked or `Goexit`-ed first) | `outcomeCancelled` | The breaker has no evidence about the dependency — only that its own hook broke. Treating a hook bug as dependency evidence would let observability code trip a circuit nothing is wrong behind. |
| `op` started but never returned (`op` itself called `Goexit`) | `outcomeFailure` | Symmetric with ADR-0003's treatment of an ordinary panic in `op`: bastion cannot distinguish a caller bug from dependency-triggered corruption, and an operation that never completed is not evidence the dependency is healthy. |
| `op` panicked | `outcomeFailure`, unconditionally | Unchanged from ADR-0003. |
| `op` returned normally | Existing classification (`ctx.Err()`, then `classify`) | Unchanged. |

For a Half-Open probe specifically, `outcomeCancelled`'s existing behavior
(ADR-0005) already does the right thing here without new code: the slot is
freed, the window's own verdict stays undecided, and a sibling probe or
ADR-0004's staleness lease resolves it — exactly the leak this ADR closes.

**A hook's panic is re-raised to `Execute`'s caller, after bookkeeping
completes, with one priority rule: if `op` itself also panicked, `op`'s
panic wins.** ADR-0003 already made that an unconditional contract; changing
it because a hook *also* misbehaved on the same call would be surprising
and would not serve the caller better — the operation's own panic is the
more actionable signal in that compound case. A hook panic that occurs
without an `op` panic is not swallowed by that rule; it surfaces on its own.

## The alternative that was rejected

**Swallowing hook panics.** Rejected on point 3 above: bastion has no other
channel a swallowed panic could surface through, so "swallow" means "hide
forever." A caller's hook has exactly the same claim to correctness as their
`op` does, and `op`'s panics are never swallowed.

**Letting `op` still run after an admission-hook panic, by recovering and
continuing in place.** Rejected on point 1: recovering an ordinary panic
*can* let execution continue past it, which momentarily looks like the
answer — but `Goexit` cannot be recovered from in any sense that lets
execution continue, so "op still runs" cannot be made to hold for both
failure modes at once. Making the admission hook's failure modes behave
identically (op does not run either way; the breaker's accounting is
resolved either way) is simpler than a contract that holds for panics and
silently does not for `Goexit`.

## Consequences

- **A host whose `OnStateChange` handler has a systematic bug — panics on
  every call — will see every call through that breaker accounted as
  `outcomeCancelled`.** The circuit will not trip from this alone (correct:
  nothing is wrong with the dependency), but it also will not learn anything
  about the dependency's health while the hook stays broken, since `op`
  never runs. This is the honest consequence of "no evidence" rather than a
  gap: the dependency's actual state is genuinely unknown to the breaker
  under these conditions, and pretending otherwise in either direction would
  be worse.
- **`CallEvent.Err` gains a value it did not have before**: `"bastion: a
  hook panicked before the operation could run"`, for the `outcomeCancelled`
  case caused by a hook rather than by `ctx.Err()`. A host distinguishing
  cancellation from this new case by inspecting `Err` (rather than relying on
  `Counted` alone, which was already `false` for both) needs to know both
  shapes exist.
- `Execute`'s doc comment gains a hook-panic paragraph; `doc.go`'s "never
  originates a panic of its own" line already only ever claimed to describe
  bastion's own code, and still does — this ADR is about what bastion does
  with a panic that started in the host's code, not about bastion raising
  one.

## Reopening criterion

Not deferred. This is the decision issue #46 was blocked on.

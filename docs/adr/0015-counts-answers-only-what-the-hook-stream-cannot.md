# ADR-0015: Counts answers only what the hook stream cannot, and adds nothing to the hot path

## Status
Accepted

## Context

`Breaker` exposes `Name()` and `State()`. Neither answers the two questions
an operator actually asks while looking at a misbehaving circuit (issue
#53): *how close is this to tripping right now* (consecutive failures
against the threshold), and *how long has it been open*. Answering either
today means a consumer mirrors bastion's own internal counters from the
`Hooks` event stream — reimplementing state bastion already maintains
correctly, and getting the concurrency right a second time. `sony/gobreaker`
ships `Counts()` for the same reason.

**The scope question the issue itself flags as unresolved: does `Counts`
also carry lifetime totals** — requests, successes, failures, rejections
since construction — the way the issue's own sketch tentatively lists them?
That needs a real answer, not a default, because it is not free: a counter
incremented on every `Execute` is a write on the hot path, and it comes with
an undecided reset question (reset on state change, like gobreaker's own
counters, or accumulate forever) that the issue explicitly declines to
prejudge.

**The distinction that settles it: `Hooks` already answers the totals
question, completely, for a host willing to count events — and cannot
answer the current-state questions at all, no matter how much counting a
host does.** `Hooks.OnCall` and `Hooks.OnReject` fire once per outcome; a
host that wants "requests since construction" or "rejections since the
circuit last opened" gets an exact answer by counting those events — no
mirroring of bastion's internal fields required, because a running total is
arithmetic a host can do correctly from a stream it already receives in
full. `ConsecutiveFailures` and `OpenedAt` are different in kind: they are
not sums over the event stream, they are *bastion's own working state at
this instant* — whether the last event was a success that zeroed the
counter, whether the breaker moved on to Half-Open since the last state
change fired, whether `openTimeout` has elapsed since without a call having
happened yet to notice. A host cannot reconstruct that from events without
re-deriving `effectiveState`'s own logic, which is exactly the "getting the
concurrency right a second time" problem #53 opens with.

So the line is not "totals versus non-totals" by convenience; it is
"derivable by counting hook events" versus "not derivable at all without
duplicating bastion's own logic." Everything in the first category already
has a complete, correct answer today, via hooks that already exist. Only the
second category is an actual gap.

## Decision

```go
// Counts is a snapshot of a Breaker's current bookkeeping, taken under the
// same lock every call already uses (FR-13, NFR-01). It answers what the
// Hooks event stream cannot -- what this breaker's state looks like right
// now -- not what has happened over time, which a host already gets exactly
// by counting Hooks.OnCall and Hooks.OnReject events (ADR-0015).
type Counts struct {
	// State is the breaker's current state, including a timeout already
	// elapsed -- the same value State() would return, computed once so
	// Counts is an internally consistent snapshot rather than several
	// separately-read fields.
	State State

	// ConsecutiveFailures counts toward FailureThreshold. Zero unless State
	// is StateClosed (FR-02): a probe or a rejection is not itself a
	// consecutive failure, and this field is not "how many failures led to
	// the current state," only "how many more would trip it from here."
	ConsecutiveFailures int

	// FailureThreshold is the value given to WithFailureThreshold (or its
	// default), included so a caller holding only a Counts value -- not the
	// options New was given -- can compute how close ConsecutiveFailures is
	// to tripping the circuit without needing to have kept that number
	// itself.
	FailureThreshold int

	// OpenedAt is when the current Open episode began. Zero unless State is
	// StateOpen -- not "the last time this breaker was Open," which would
	// stay stale and misleading long after it recovered.
	OpenedAt time.Time
}

// Counts returns a snapshot of b's current bookkeeping (FR-09, FR-13).
func (b *Breaker) Counts() Counts {
	b.mu.Lock()
	defer b.mu.Unlock()
	eff := b.effectiveState(b.clock.Now())
	c := Counts{
		State:               eff,
		ConsecutiveFailures: b.consecutiveFailures,
		FailureThreshold:    b.failureThreshold,
	}
	if eff == StateOpen {
		c.OpenedAt = b.openedAt
	}
	return c
}
```

Every field is already-stored state; `Counts` adds no new field to
`Breaker`, no new write to `admit` or `complete`, and therefore nothing to
`Execute`'s hot path. `docs/benchmarks.md` does not need new numbers for
this change — the issue's own conditional ("re-run if the snapshot adds a
field to the hot path") does not trigger, and that is stated here rather
than silently skipped.

No lifetime totals are added. A host that wants them counts
`Hooks.OnCall`/`Hooks.OnReject` events itself, which is already a complete
answer requiring no change to this library.

## The alternative that was rejected

**Add lifetime totals (requests, successes, failures, rejections) to
`Counts`, resetting on every state change, matching `gobreaker`'s own
shape.** Rejected: it duplicates what `Hooks` already delivers completely,
adds a write to `Execute`'s hot path for every call (the cost the issue's
own "Done when" checklist flags as worth a fresh benchmark, not something to
wave through), and inherits an unresolved reset-semantics question this
issue explicitly declined to prejudge. If a concrete need for
library-maintained totals surfaces — a host that cannot reasonably wire up
hooks just to count events, say — that is new evidence and a new ADR, not a
default folded into this one for symmetry with another library's shape.

**A "since this episode opened" rejection counter**, narrower than full
lifetime totals and closer to the issue's own "how many calls has it
rejected since [opening]" phrasing. Considered, and rejected on a different
ground than the totals question: it is derivable completely and correctly
by counting `Hooks.OnReject` events between the two `Hooks.OnStateChange`
events that bracket an Open episode — no mirroring of internal state
required, the same reasoning that rules out totals generally. Adding it
would also add a new mutable field to `Breaker` that must be reset at
exactly the right transition, in the same class of state-machine-adjacent
change CLAUDE.md already singles out as warranting complex-task treatment
regardless of diff size — not justified for something a host can already
compute for free from data it already receives.

## Consequences

- `Counts` is returned by value; nothing about it lets a caller mutate
  `Breaker` state, and taking it under `b.mu` means it is an internally
  consistent view, never a field-by-field race with a transition in
  progress.
- A caller who wants totals or a since-Open rejection count implements it
  from `Hooks`, in their own code, with no bastion change required — this
  was already true before this ADR and remains the documented path.

## Reopening criterion

A concrete case surfaces where counting hook events is not a viable way to
get a total or a since-Open count — a host that cannot wire hooks at all,
say, or a correctness argument that event-counting can drift from bastion's
own internal total in some case this ADR did not consider. Today, none has.

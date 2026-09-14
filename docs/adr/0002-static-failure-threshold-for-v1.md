# ADR-0002: A static consecutive-failure threshold for v1; adaptive deferred to v2

## Status
Accepted

## Context

FR-02 (static) and FR-03 (adaptive percentage over volume) both name a valid
way to decide a circuit has seen enough failures to open. They are not
compatible defaults — a breaker configured with a hard count of 5 and a
breaker configured with "30% of the last 100 calls" behave differently at
every traffic volume — so v1 has to pick one, and REQUIREMENTS.md already
marks FR-02 Must and FR-03 Should(v2). This ADR is the reasoning, not a new
conclusion.

## Decision

v1 ships FR-02 only: `WithFailureThreshold(n)`, a plain consecutive-failure
counter that resets to zero on any success and opens the circuit the instant
it reaches `n` (default 5, `defaultFailureThreshold` in options.go).

FR-03 is out of scope for v1's code, though not for v1's design: `State`,
`Hooks`, and the transition logic are written so that swapping the threshold
strategy later does not require breaking either.

## The alternative that was rejected

**Building the adaptive percentage threshold now, with the static count as a
special case of it (a 100%-of-1 rate).** Rejected on the basis of what it
would cost to test correctly. A percentage threshold needs a volume floor —
REQUIREMENTS.md's own README example names the trap: three calls with one
failure is a 33% error rate that should not open a healthy circuit — a
sliding or bucketed window, and a decision about what happens when volume is
too low to have an opinion yet. Getting a sliding window's boundary
conditions right, deterministically, on a fake clock, is a materially larger
testing surface than a counter and a comparison, and B1's actual job is
proving the *state machine* — Closed/Open/Half-Open and their transitions —
is correct. Folding an unsolved measurement problem into that would risk
getting both wrong instead of one right.

## Consequences

- A host running bastion in front of a low-traffic dependency gets exactly
  the behavior FR-02 promises: five failures in a row, however spaced apart
  in time, opens the circuit. A dependency with occasional errors mixed into
  heavy traffic gets the same five-in-a-row rule, which is blunter than a
  rate would be — a 2% steady error rate under real load will not trip a
  consecutive counter, and that is deliberately how FR-02 behaves.
- `WithFailureThreshold`'s doc comment and this ADR are the only places this
  limitation is written down; a caller migrating a high-volume dependency
  from a percentage-based breaker in another language needs to read one of
  them before assuming parity.
- The classification hook (FR-04, WithIsFailure) is unaffected either way —
  it decides what counts as a failure, not how failures are aggregated — so
  nothing here blocks B2.

## Reopening criterion

FR-03 is picked back up when either is true:
- A consumer of bastion reports needing rate-based tripping for a
  high-volume dependency where consecutive-count behavior is measurably
  wrong for their traffic shape, or
- B8 arrives on the roadmap (REQUIREMENTS.md §7) and nothing above has
  already forced the question sooner.

When it is reopened, the decision to make is whether FR-03 is a second
`Option` alongside `WithFailureThreshold` or a replacement for it — that
choice is not prejudged here.

# Architecture Decision Records

Every structural decision is recorded here. **An ADR is never edited to reflect
a later change of mind.** It is amended in place with a section naming what
superseded which part, so the reasoning that was current at the time stays
readable.

CI enforces the mechanical form: an existing ADR may gain lines, never lose
them. The one legitimate exception is a genuine typo, and it is taken by
labelling the pull request `adr-typo` — visibly, on the record.

New records start from [`TEMPLATE.md`](TEMPLATE.md), which is deliberately not
named `0000-` so it does not read as a decision or trip the index check.

## Index

| ADR | Title | Status | Amended by |
| --- | --- | --- | --- |
| [`0001`](0001-entry-point-is-a-free-generic-function-named-execute.md) | The entry point is a free generic function named Execute | Accepted | — |
| [`0002`](0002-static-failure-threshold-for-v1.md) | A static consecutive-failure threshold for v1; adaptive deferred to v2 | Accepted | — |
| [`0003`](0003-a-panic-always-counts-as-a-failure-and-is-re-raised.md) | A panic in the operation always counts as a failure, and is re-raised unchanged | Accepted | — |
| [`0004`](0004-a-stale-half-open-probe-times-out-back-to-open.md) | A stale half-open probe expires back to Open, on the same timeout | Accepted | — |

## Open questions

Deferred deliberately and listed rather than left implicit: an undecided
question that looks decided is the one that gets implemented by accident.

**This table is the only list of them.** Code comments cite a question by its
name, never by a position in a list — a `§8.3` resolves to a different question
the moment the list is reordered, and nothing in this repository checks section
numbers. The numbers in the first column are for conversation, not for citation.

| # | Question | Needed by |
| --- | --- | --- |
| 1 | How context cancellation is accounted for: neither a false failure that opens a circuit on a healthy dependency, nor a false success that hides a slow one (FR-05) | B2 |
| 2 | Retry inside the breaker or composed around it, and which order the library recommends. The current position is that they stay decoupled, and the reasoning has to be written down before the code assumes it (FR-06) | B3 |
| 3 | How the fallback of FR-08 is supplied. It is typed by the call's return value, and a method cannot introduce a type parameter its receiver lacks — see the note at the end of [`options.go`](../../options.go) | B5 |
| 4 | Whether the per-operation timeout of FR-07 needs library code at all, given that it is `context.WithTimeout` | B3 |
| 5 | Where bastion plugs in first. The gateway is the natural candidate — a reverse proxy without resilience is a single point of failure for everything behind it — and the decision belongs on the record rather than in a conversation. The record also fixes the two host-side rules of [`REQUIREMENTS.md`](../../REQUIREMENTS.md) §5.1: `ErrOpenState` maps to `503` with `Retry-After`, and a rejected outbound call does not re-charge the client's `moat` rate-limit budget | B7 |

Four questions that blocked B1 — the entry point's name and signature, the
static-versus-adaptive threshold, panic accounting, and stale half-open
recovery — are resolved as [ADR-0001](0001-entry-point-is-a-free-generic-function-named-execute.md)
through [ADR-0004](0004-a-stale-half-open-probe-times-out-back-to-open.md) and
have been removed from this table; see the Index above.

## Reopening criteria on the record

Decisions carrying an explicit trigger, so "revisit later" is not a synonym for
never.

| ADR | Reopen when |
| --- | --- |
| [`0002`](0002-static-failure-threshold-for-v1.md) | A consumer reports needing rate-based tripping for a high-volume dependency where consecutive-count behavior is measurably wrong for their traffic shape, or B8 arrives on the roadmap |
| [`0004`](0004-a-stale-half-open-probe-times-out-back-to-open.md) | A real dependency's healthy response time is close to or exceeds a reasonable `openTimeout`, causing the flapping described in its Consequences section |

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
| [`0005`](0005-context-cancellation-is-detected-by-reading-the-outer-ctx.md) | Context cancellation is detected by reading the outer ctx, not by matching the returned error | Accepted | — |
| [`0006`](0006-retry-composes-around-the-breaker-not-inside-it.md) | Retry composes around the breaker, not inside it | Accepted | — |
| [`0007`](0007-no-dedicated-timeout-helper.md) | No dedicated timeout helper — FR-07 is satisfied by documenting the pattern | Accepted | — |
| [`0008`](0008-fallback-is-a-post-execute-call-site-function.md) | Fallback is a post-Execute, call-site function — never nested inside op | Accepted | — |

## Open questions

Deferred deliberately and listed rather than left implicit: an undecided
question that looks decided is the one that gets implemented by accident.

**This table is the only list of them.** Code comments cite a question by its
name, never by a position in a list — a `§8.3` resolves to a different question
the moment the list is reordered, and nothing in this repository checks section
numbers. The numbers in the first column are for conversation, not for citation.

| # | Question | Needed by |
| --- | --- | --- |
| 1 | Where bastion plugs in first. The gateway is the natural candidate — a reverse proxy without resilience is a single point of failure for everything behind it — and the decision belongs on the record rather than in a conversation. The record also fixes the two host-side rules of [`REQUIREMENTS.md`](../../REQUIREMENTS.md) §5.1: `ErrOpenState` maps to `503` with `Retry-After`, and a rejected outbound call does not re-charge the client's `moat` rate-limit budget | B7 |

Eight questions are resolved and removed from this table; see the Index above.
Four blocked B1 — the entry point's name and signature, the
static-versus-adaptive threshold, panic accounting, and stale half-open
recovery ([ADR-0001](0001-entry-point-is-a-free-generic-function-named-execute.md)
through [ADR-0004](0004-a-stale-half-open-probe-times-out-back-to-open.md)).
One blocked B2 — how context cancellation is accounted for
([ADR-0005](0005-context-cancellation-is-detected-by-reading-the-outer-ctx.md)).
Two blocked B3 — the retry/breaker composition order, and whether the
per-operation timeout needs library code at all
([ADR-0006](0006-retry-composes-around-the-breaker-not-inside-it.md),
[ADR-0007](0007-no-dedicated-timeout-helper.md)).
One blocked B5 — how the fallback of FR-08 is supplied
([ADR-0008](0008-fallback-is-a-post-execute-call-site-function.md)).

## Reopening criteria on the record

Decisions carrying an explicit trigger, so "revisit later" is not a synonym for
never.

| ADR | Reopen when |
| --- | --- |
| [`0002`](0002-static-failure-threshold-for-v1.md) | A consumer reports needing rate-based tripping for a high-volume dependency where consecutive-count behavior is measurably wrong for their traffic shape, or B8 arrives on the roadmap |
| [`0004`](0004-a-stale-half-open-probe-times-out-back-to-open.md) | A real dependency's healthy response time is close to or exceeds a reasonable `openTimeout`, causing the flapping described in its Consequences section |

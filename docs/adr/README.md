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
| [`0006`](0006-retry-composes-around-the-breaker-not-inside-it.md) | Retry composes around the breaker, not inside it | Accepted | [`0011`](0011-retry-skips-the-wait-after-a-breaker-rejection.md) (point 2, partial) |
| [`0007`](0007-no-dedicated-timeout-helper.md) | No dedicated timeout helper — FR-07 is satisfied by documenting the pattern | Accepted | — |
| [`0008`](0008-fallback-is-a-post-execute-call-site-function.md) | Fallback is a post-Execute, call-site function — never nested inside op | Accepted | — |
| [`0009`](0009-bastion-plugs-in-first-at-the-gateway.md) | bastion plugs in first at the gateway, and the two host-side rules that composition needs | Accepted | — |
| [`0010`](0010-a-hook-panic-never-corrupts-bookkeeping.md) | A hook panic never corrupts bookkeeping; op never running is accounted like a cancelled call, not a failure | Accepted | — |
| [`0011`](0011-retry-skips-the-wait-after-a-breaker-rejection.md) | Retry skips the wait after a breaker rejection, via a caller-overridable retriability predicate | Accepted | — |
| [`0012`](0012-retrys-total-elapsed-time-is-bounded-by-the-callers-context.md) | Retry's total elapsed time is bounded by the caller's context, not a MaxElapsedTime field | Accepted | — |
| [`0013`](0013-provenance-attests-the-source-tree-not-a-compiled-artifact.md) | Provenance attests a reproducible source tarball, not a compiled artifact | Accepted | — |
| [`0014`](0014-execute-gains-an-empty-call-option-slot.md) | Execute gains an empty CallOption slot; Retry and Fallback do not | Accepted | — |
| [`0015`](0015-counts-answers-only-what-the-hook-stream-cannot.md) | Counts answers only what the hook stream cannot, and adds nothing to the hot path | Accepted | — |
| [`0016`](0016-the-single-mutex-throughput-ceiling-is-accepted-not-optimized.md) | The single-mutex throughput ceiling is measured and accepted, not optimized away | Accepted | — |
| [`0017`](0017-manual-trip-and-reset.md) | Manual Trip is sticky until Reset; Reset clears everything; both are visible as Manual | Accepted | — |

## Open questions

Deferred deliberately and listed rather than left implicit: an undecided
question that looks decided is the one that gets implemented by accident.

**This table is the only list of them.** Code comments cite a question by its
name, never by a position in a list — a `§8.3` resolves to a different question
the moment the list is reordered, and nothing in this repository checks section
numbers. The numbers in the first column are for conversation, not for citation.

| # | Question | Needed by |
| --- | --- | --- |

None open. See the Index above for how each of the nine original questions
was resolved. [ADR-0010](0010-a-hook-panic-never-corrupts-bookkeeping.md),
[ADR-0011](0011-retry-skips-the-wait-after-a-breaker-rejection.md),
[ADR-0012](0012-retrys-total-elapsed-time-is-bounded-by-the-callers-context.md),
[ADR-0013](0013-provenance-attests-the-source-tree-not-a-compiled-artifact.md),
[ADR-0014](0014-execute-gains-an-empty-call-option-slot.md),
[ADR-0015](0015-counts-answers-only-what-the-hook-stream-cannot.md),
[ADR-0016](0016-the-single-mutex-throughput-ceiling-is-accepted-not-optimized.md),
and
[ADR-0017](0017-manual-trip-and-reset.md)
are not answers to questions from this table — they answer defects and
open items the B9/B10 audit found (issues #46, #47/#48, #49, #51, #52, #53,
#55, and #54), not questions deferred from an earlier phase.
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
One blocked B7 — where bastion plugs in first, and the two host-side rules
that composing with `moat` needs
([ADR-0009](0009-bastion-plugs-in-first-at-the-gateway.md)).

## Reopening criteria on the record

Decisions carrying an explicit trigger, so "revisit later" is not a synonym for
never.

| ADR | Reopen when |
| --- | --- |
| [`0002`](0002-static-failure-threshold-for-v1.md) | A consumer reports needing rate-based tripping for a high-volume dependency where consecutive-count behavior is measurably wrong for their traffic shape, or B8 arrives on the roadmap |
| [`0004`](0004-a-stale-half-open-probe-times-out-back-to-open.md) | A real dependency's healthy response time is close to or exceeds a reasonable `openTimeout`, causing the flapping described in its Consequences section |

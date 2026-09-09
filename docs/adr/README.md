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

No decisions recorded yet. Each open question below becomes a record before the
phase that depends on it — that ordering is the point of the exercise, since a
decision embedded in code is one nobody can review.

## Open questions

Deferred deliberately and listed rather than left implicit: an undecided
question that looks decided is the one that gets implemented by accident.

**This table is the only list of them.** Code comments cite a question by its
name, never by a position in a list — a `§8.3` resolves to a different question
the moment the list is reordered, and nothing in this repository checks section
numbers. The numbers in the first column are for conversation, not for citation.

| # | Question | Needed by |
| --- | --- | --- |
| 1 | Static failure threshold for v1, adaptive percentage for v2 — simplicity of implementation and testing against robustness across traffic volumes (FR-02, FR-03) | B1 |
| 2 | How context cancellation is accounted for: neither a false failure that opens a circuit on a healthy dependency, nor a false success that hides a slow one (FR-05) | B2 |
| 3 | Retry inside the breaker or composed around it, and which order the library recommends. The current position is that they stay decoupled, and the reasoning has to be written down before the code assumes it (FR-06) | B3 |
| 4 | The public entry point's name and signature — `Execute` against `Do` against `Run`, and whether it is a function or a method. It cannot be renamed after release without breaking every caller (FR-01) | B1 |
| 5 | How the fallback of FR-08 is supplied. It is typed by the call's return value, and a method cannot introduce a type parameter its receiver lacks — see the note at the end of [`options.go`](../../options.go) | B5 |
| 6 | Whether the per-operation timeout of FR-07 needs library code at all, given that it is `context.WithTimeout` | B3 |
| 7 | Where bastion plugs in first. The gateway is the natural candidate — a reverse proxy without resilience is a single point of failure for everything behind it — and the decision belongs on the record rather than in a conversation. The record also fixes the two host-side rules of [`REQUIREMENTS.md`](../../REQUIREMENTS.md) §5.1: `ErrOpenState` maps to `503` with `Retry-After`, and a rejected outbound call does not re-charge the client's `moat` rate-limit budget | B7 |
| 8 | How a panic in the wrapped operation is accounted for (FR-11). Releasing the lock is not the question — `defer` settles that — the question is whether a panic counts as a failure. It is a bug in the caller's code, not evidence about the remote dependency, and counting it opens a circuit on a service that answered correctly | B1 |
| 9 | How a half-open probe that never returns is recovered (FR-12). With an allowance of one, a single hung call leaves the circuit admitting nothing, forever, while reporting itself as recovering. The candidates — re-arming the allowance after the open timeout elapses again, against requiring the caller to bound the probe with its own context — trade a surprise for an obligation | B1 |

## Reopening criteria on the record

Decisions carrying an explicit trigger, so "revisit later" is not a synonym for
never.

| ADR | Reopen when |
| --- | --- |

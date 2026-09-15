# ADR-0018: A metrics adapter is a documented pattern, not a shipped satellite module

## Status
Accepted

## Context

`Hooks` is the right primitive, and the reason NFR-03 holds: no metrics
library appears anywhere in this module. But every consumer wanting
Prometheus or OpenTelemetry metrics writes the same adapter — counter names,
label cardinality, which event maps to which instrument — and getting
cardinality wrong is a real way a metrics backend falls over (issue #58).
`moat`'s `redisstore` establishes the structural answer this ecosystem
already uses for exactly this shape of problem: a separate module
(`bastion/prombastion`, say), so the dependency never reaches the core's
`go.sum` unless a consumer asks for it.

**The cost is real and already has a track record in this ecosystem, not a
hypothetical one.** Adopting a satellite module means adopting the CI
machinery `ci.yml`'s own header describes as deliberately absent for this
single-module repository: a module matrix, a `satellite-resolution` job
running with `GOWORK=off`, and the release-ordering discipline in
`RELEASING.md` that goes with it. `moat`'s own `redisstore/v0.2.0` shipped
broken without that job — a satellite requiring a core version it did not
actually satisfy, with every local signal green. That is not a risk this
ADR is guessing at; it is what happened the one time a sibling project
skipped it.

**The benefit, weighed against a genuine alternative, turns out smaller than
it first looks.** A satellite module's entire justification is keeping a
metrics library's dependency out of the core's `go.sum` — but `Hooks`
already gives any consumer everything a satellite module would, with no
bastion-owned code required at all: `OnStateChange`, `OnCall`, and
`OnReject` carry everything a Prometheus counter or an OTel instrument needs
to record. The problem this issue actually names is not "no mechanism
exists" — one already does, and always has — it is "no consumer has been
shown a correct example, so everyone reinvents one, and cardinality mistakes
are the predictable result of reinventing it under time pressure." A
worked, correct example closes that gap completely, at zero ongoing
maintenance cost and zero CI-machinery adoption, the same way
[ADR-0007](0007-no-dedicated-timeout-helper.md) already declined a
`WithTimeout` helper in favor of documenting the three-line pattern instead
— this is the identical shape of decision, not a new principle invented for
this ADR.

**And unlike this session's other "add it now while it's cheap" decisions
(ADR-0014's `CallOption` slot), there is no ossification pressure pushing
toward acting now.** A satellite module is purely additive: nothing about
adding `bastion/prombastion` in a year costs more than adding it today, and
nothing in the core's public API needs to change to make it possible at any
point — `Hooks` already is the complete, stable extension point a future
satellite would build on. There is no `apidiff`-flagged compatibility
window closing here, the way there genuinely was for `Execute`'s signature.

No real consumer has asked for a shipped adapter. Building the CI machinery
and committing to the release-ordering discipline ahead of that — for a
library whose own stated priority order puts correctness and simplicity
before a convenience nothing currently blocks — is exactly the "abstraction
the current problem does not require" the general engineering rules warn
against, with the sharper edge that this one, unlike most, has already been
observed to fail once in a sibling project when done carelessly.

## Decision

**No satellite module, for now.** Instead: a documented, worked example of
wiring `Hooks` to a Prometheus-shaped metrics backend, showing the counter
names, the label set, and — the actual point of the exercise — where
cardinality would blow up if `Name` (or any other unbounded value) were used
as a label directly instead of a bounded one. This lives in `README.md`
alongside the rest of the "Usage" section, not in a new module, and ships no
dependency of any kind.

The core's `boundaries` job is untouched by this decision — there is no code
to have it check.

## The alternative that was rejected

**Ship `bastion/prombastion` as a first satellite module now, with the
`satellite-resolution` CI job and `RELEASING.md` release-ordering rule
adopted ahead of it, per the issue's own "Done when" framing of picking a
starting point.** Weighed seriously — the ecosystem precedent is real, and
`moat`/`cairn` show it is a solved shape, not an experimental one — but
rejected on the absence of the two things that would justify paying a
non-trivial, ongoing CI cost ahead of need: a demonstrated consumer and a
capability `Hooks` does not already provide. Revisit the moment either
appears.

## Consequences

- A consumer who wants Prometheus or OTel metrics today writes the adapter
  themselves, guided by the documented example rather than starting from
  nothing. This is materially better than the status quo before this ADR
  (no example at all) without being as good as a maintained module — a
  real, stated trade-off, not a free win.
- If a satellite module is adopted later, the CI machinery and
  release-ordering discipline this ADR declines to adopt now are exactly
  what issue #58 already specified; nothing here needs to be re-litigated,
  only actually built.

## Reopening criterion

A real consumer need for a maintained adapter — reported friction, a
cardinality incident traced back to the absence of one, or a concrete
request — or evidence the documented example is not sufficient guidance in
practice. Today, neither has been reported.

# ADR-0020: Breaker state does not gate a readiness probe

## Status
Accepted

## Context

Nothing in this repository mentioned `readinessProbe`, `/health/ready`, or
liveness — not the README, not `REQUIREMENTS.md`, not `SECURITY.md`, not any
of the nineteen ADRs before this one. For a library whose stated home is "in
front of every outbound call in a reverse proxy", deployed in Kubernetes, that
is a gap a real adopter walks into immediately (issue #80): `Counts()` reports
`StateOpen` — should the pod stop accepting traffic?

The intuitive answer is yes. `Counts()` exists precisely so an operator can see
this, the state is right there, and wiring it into the probe the orchestrator
already calls is three lines. The intuition is wrong, and it is wrong in a way
that only shows up during the incident it would cause.

## Decision

**An open circuit must not, on its own, make an instance report not-ready.**

The argument is that an open circuit is a *handled* failure. That is the entire
purpose of this library: the call fails in nanoseconds instead of occupying a
goroutine and a connection for a timeout's worth of wall clock, the caller gets
a refusal it can act on, and **every route that does not touch that dependency
is still being served normally**. Reporting not-ready throws that away — it
removes the instance from rotation over a dependency the breaker is already
degrading around gracefully, and takes the healthy routes down with it.

Both real topologies make it worse rather than better, which is what settles
this:

- **One replica.** Reporting not-ready removes the only instance. A partial
  outage — one dependency degraded, everything else fine — is converted into a
  total one. The breaker's entire contribution is inverted.
- **Many replicas against a shared dependency.** Every instance observes the
  same failures at roughly the same time, so every breaker opens at roughly the
  same time, so every instance reports not-ready at roughly the same time. The
  orchestrator has nowhere left to route. Same total outage, reached by a longer
  and more confusing path — and now with a rolling restart likely on top of it.

Liveness is not a near miss of this decision, it is further from it: a failing
liveness probe restarts the container. Restarting does not repair a dependency
outside the process; it discards the breaker's accumulated evidence, kills the
in-flight requests that were being served correctly, and returns to `StateClosed`
to rediscover the same failures from scratch. Breaker state must never reach a
liveness probe.

**What to expose instead.** `Counts()` belongs on the metrics or debug surface
the host already has — an `expvar` endpoint, a Prometheus scrape, whatever is
being collected. The distinction is who the reader is: readiness is consumed by
a *scheduler* deciding where to send traffic, and metrics are consumed by an
*operator* deciding whether to page someone. An open circuit is unambiguously
the second: it is something a human should know about and possibly act on, and
nothing an orchestrator can improve by moving traffic around.

## The alternative that was rejected

**Gate readiness on breaker state, for the dependency the service genuinely
cannot function without.** This is the strongest form of the case, and it is not
empty. A breaker is a *better-evidenced* signal than the live ping most readiness
probes use: it reflects the accumulated outcomes of real traffic rather than one
synthetic query that may have got lucky, and it costs no connection to read.
Where an instance truly cannot serve any route without that dependency,
"not ready" is arguably honest.

Rejected because honesty about the instance is not the property a readiness probe
is for. The probe's answer is an instruction to a scheduler — "send traffic
elsewhere" — and both topologies above show there is no elsewhere: the sibling
instances are failing for the same reason at the same moment, or there are no
siblings. An honest signal that produces a worse outcome than silence is not
worth sending, and the operator who needs that honesty is already reading the
metrics surface where it does belong.

**Gate readiness only when *every* breaker is open**, as a narrower version of
the same idea. Considered, and it fails for the same reason rather than a
different one: correlated failure across instances is what makes the scheduler
powerless, and requiring more breakers to be open does not decorrelate anything.
It only delays the same outcome and makes it harder to explain afterwards.

## Consequences

- A host wiring `Counts()` into a readiness probe is doing something this ADR
  advises against; nothing in the library prevents it, because nothing in the
  library can see the probe. The guidance is the whole mechanism here.
- An instance that has *never* reached its dependency — a fresh pod with a
  misconfigured endpoint — is a real readiness question that this decision does
  not answer and the breaker structurally cannot: at startup the circuit is
  `StateClosed` with no counts, indistinguishable from healthy, because it has
  no evidence yet. A startup or readiness check that actually reaches the
  dependency once is the right tool for that, and it remains the host's.
- `Counts`'s own godoc points here, since holding `StateOpen` is when the
  question occurs to a reader.

## Reopening criterion

A deployment where the guarded dependency is *per-instance* rather than shared —
a sidecar, a node-local cache, a connection pool bound to one host — so that one
instance's circuit opening while its siblings stay closed is genuine evidence
that this instance specifically should stop receiving traffic and the others can
absorb it. The reasoning above turns on correlated failure; a per-instance
dependency breaks that correlation and with it the argument.

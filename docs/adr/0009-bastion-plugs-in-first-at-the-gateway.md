# ADR-0009: bastion plugs in first at the gateway, and the two host-side rules that composition needs

## Status
Accepted

## Context

Every prior ADR settled how `bastion` behaves in isolation. None of them said
where it is actually used first, and REQUIREMENTS.md §5.1 already left two
rules unresolved, pointing here: turning `ErrOpenState` into a response, and
not double-charging a client's rate-limit budget when bastion rejects a call
`moat` already admitted. Both are the host's code, not either library's — but
"the host" needed a name before those rules could be anything but
hypothetical.

The candidate is the authentication gateway that reverse-proxies to
`task-api`. A reverse proxy with no resilience in front of the service it
forwards to is a single point of failure for everything behind it: one slow
or failing backend call blocks the goroutine handling it, and enough of those
exhaust the proxy's own capacity — the proxy goes down because the thing
*behind* it is unhealthy, which is precisely the cascade this library exists
to stop.

## Decision

**The gateway's reverse-proxy path to `task-api` is where `bastion` plugs in
first.** One named `*bastion.Breaker` guards the outbound call the gateway
makes for every proxied request.

That work happens in the gateway's own repository, not here — issue #32
tracks it there, not in bastion. What this ADR fixes is the two host-side
rules the gateway's implementation is bound by, so they exist on a record
instead of being decided ad hoc mid-implementation:

1. **`ErrOpenState` (and `ErrTooManyRequests`) map to an HTTP `503`, carrying
   `Retry-After`, never a `500`.** A `500` tells the caller their request
   broke something and to stop; an open circuit is bastion correctly refusing
   to make things worse, and the caller should come back shortly. Conflating
   the two teaches every client of the gateway to treat "the dependency is
   healthy again in a few seconds" the same as "something is broken here,"
   which is the wrong signal in both directions.
2. **A call bastion rejects does not re-charge the client's `moat` rate-limit
   budget.** `moat`'s limiter has already admitted the request by the time
   it reaches the outbound call; bastion refusing to make that call is not a
   second offense by the same client, and charging it again punishes a
   caller for a backend outage they did not cause.

Both are ordinary application code in the gateway's request-handling path —
an error-mapping function and an ordering guarantee (limiter runs once, on
the way in) — not a hook, adapter, or shared type either library exposes for
the purpose. **`bastion` still does not import `moat`, and this ADR does not
change that (IR-05).** The two meet only in the gateway's own code, exactly
as REQUIREMENTS.md §5.1 already diagrammed:

```
inbound request ──▶ moat middleware chain ──▶ gateway handler
                    (rate limit, CSRF,              │
                     headers, validation)           │  outbound
                                                    ▼
                                           bastion-guarded call ──▶ task-api
```

## The alternative that was rejected

**Waiting for the gateway integration to exist before writing this down, and
letting the two rules get decided inside that implementation instead.** That
is what REQUIREMENTS.md §5.1 explicitly declined to do ("Neither belongs in a
library. Both belong in the ADR that records where bastion plugs in first").
A rule discovered while writing a handler is a rule nobody can point to
before the code review that catches — or misses — a deviation from it; a rule
on the record before the first line of that handler is written is one the
code review is checking *against*.

## Consequences

- The gateway repository's own implementation (issue #32, tracked there) is
  answerable to this ADR: a proxy handler that maps `ErrOpenState` to
  anything other than `503` + `Retry-After`, or that re-runs the rate
  limiter on a bastion rejection, is a defect against a decision already on
  record, not a fresh design question.
- `bastion` itself gains no code from this ADR. It is a boundary and a
  usage decision, not an API change — nothing here modifies `Execute`,
  `Retry`, or `Fallback`.
- The observability hooks (FR-09) are how the gateway is expected to learn
  about state changes for its own logging or metrics — wired asynchronously,
  on the host side, per IR-02. This ADR does not specify what the gateway
  does with them beyond that they run synchronously and must not block; the
  choice of sink is the gateway's own.

## Reopening criterion

Not deferred as a question — but the two rules are stated for *this*
integration specifically. A second host integrating `bastion` differently
(not behind `moat`, or with a different error-mapping convention already in
place) is not bound by this ADR's specific HTTP status mapping; it would
record its own.

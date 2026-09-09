# bastion — Requirements

**Version:** 0.1
**Status:** baseline for implementation — no domain code written yet

A Go resilience library for service-to-service calls: circuit breaker, retry
with exponential backoff and jitter, and context-based timeouts, so a failure in
one component does not cascade into everything that depends on it.

Repository: `github.com/JonasBorgesLM/bastion`

This document is binding. Every `FR-`, `NFR-` and `IR-` identifier below is
cited from commit messages, ADRs, godoc and tests, and
[`.github/scripts/check-docs.sh`](.github/scripts/check-docs.sh) fails on a
citation that resolves to nothing.

> **A requirement without a test that fails when the behaviour is removed counts
> as unimplemented.**

Cross-project citations are written qualified — `cairn/SR-18` — so they are not
silently checked against this project's numbering.

---

## 1. Purpose

With the authentication gateway acting as a reverse proxy in front of `task-api`
— and potentially other services later — every network call between components
is exposed to infrastructure failure: timeout, 5xx, connection refused. Without
protection, a failure in one component propagates in cascade to everyone
downstream of it.

`bastion` answers that in the same spirit as
[`moat`](https://github.com/JonasBorgesLM/moat): an independent Go library,
testable in isolation, pluggable into any service of the ecosystem.

### 1.1 Non-goals

Stated explicitly, because the non-goals are what stops scope arriving by
accident.

| Not this library's job | Whose it is |
| --- | --- |
| Persisting circuit state across process restarts | Out of scope entirely for v1 — a restarted process starts Closed |
| Dashboards or state visualisation | Whoever consumes the hooks of FR-09 |
| Dynamic reconfiguration at runtime (file, API) | Deferred to v2, alongside FR-03 |
| Bulkheading — bounding how many calls may be in flight at once | Out of scope for v1. It is the pattern that sits next to a circuit breaker, not inside one, and conflating them produces a breaker whose threshold and whose concurrency limit fight each other |
| Rate limiting, CSRF, secure headers | [`moat`](https://github.com/JonasBorgesLM/moat) — inbound, at the host, see §5.1 |
| Log transport and metric export | [`crier`](https://github.com/JonasBorgesLM/crier) and the host |
| HTTP routing, proxying, retries at the transport layer | The host service — `bastion` does not import `net/http` (IR-01) |

---

## 2. Actors

| Actor | Role | Assumed capability | Assumed *not* to have |
| --- | --- | --- | --- |
| Host service | Wraps its outbound calls in a breaker | Owns its own observability pipeline and error taxonomy | A metrics library `bastion` knows about, or a place to persist state |
| Remote dependency | The call being protected | May fail, hang, or recover at any time | Any awareness that a breaker exists |
| Operator | Reads the state changes the host exports | Can see what the hooks emit | Any way to reconfigure a breaker at runtime in v1 |

The last column is the one people skip and the one that matters: every design
choice below follows from `bastion` having no metrics dependency, no storage and
no runtime configuration channel.

---

## 3. Functional requirements

| ID | Requirement | Priority |
| --- | --- | --- |
| **FR-01** | Circuit state machine: Closed → Open → Half-Open, with the classic transitions — failures exceed the threshold, the open timeout expires, a probe succeeds or fails | Must (v1) |
| **FR-02** | Failure detection by static threshold: N consecutive failures | Must (v1) |
| **FR-03** | Failure detection by adaptive percentage threshold — error rate over volume | Should (v2) |
| **FR-04** | Configurable error classification: an injectable function marks an error as "does not count as a failure" or "counts as a success" | Must (v1) |
| **FR-05** | Explicit handling of `context.Context` cancellation — neither a failure nor a success of the remote service | Must (v1) |
| **FR-06** | Retry with exponential backoff and jitter, composable with the breaker but never required by it | Must (v1) |
| **FR-07** | Per-operation timeout, integrated with `context.WithTimeout` | Must (v1) |
| **FR-08** | Optional fallback, executed when the circuit is open or the call fails after retries | Should (v1) |
| **FR-09** | Observability hooks — `OnStateChange`, `OnCall`, `OnReject` — decoupled from any specific metrics library | Should (v1) |
| **FR-10** | Multiple named breakers, each with independent configuration | Must (v1) |
| **FR-11** | A panic in the wrapped operation leaves the breaker consistent — the lock released and the counters coherent — and reaches the caller unchanged | Must (v1) |
| **FR-12** | A half-open probe that never returns cannot strand the circuit: the probe allowance is recoverable, so one hung call does not leave the breaker permanently admitting nothing | Must (v1) |
| **FR-13** | The circuit's current state is readable by the host, and the read reflects a timeout that has already elapsed | Should (v1) |

One sentence each. Anything needing a paragraph is two requirements, or it
belongs in an ADR.

FR-11 and FR-12 are here because they are the two ways a circuit breaker stops
being a safety device and becomes the outage. Neither produces an error anyone
sees: a panic through a held mutex deadlocks every subsequent call to that
dependency, and a hung probe leaves a circuit that rejects everything forever
while reporting itself as recovering. Both are decided before the entry point is
written, not discovered after.

---

## 4. Non-functional requirements

| ID | Requirement |
| --- | --- |
| **NFR-01** | Thread-safe: the whole API is safe for concurrent use, validated with `go test -race` |
| **NFR-02** | Low per-call overhead, measured rather than asserted — `go test -bench -benchmem` |
| **NFR-03** | Zero external dependencies: the standard library only, enforced by the `zero-dependencies` CI job |
| **NFR-04** | Idiomatic Go API: `context.Context` first, errors as values, comparable with `errors.Is` / `errors.As` |
| **NFR-05** | Deterministic testability through an injectable [`Clock`](clock.go), so a state-transition test never calls `time.Sleep` |
| **NFR-06** | Unit coverage for every state transition and every threshold boundary case |
| **NFR-07** | Go 1.24 is the floor, and it is the lowest viable version rather than the newest — see the comment in [`go.mod`](go.mod) |
| **NFR-08** | English for all code, comments, documentation and commit messages, regardless of the language a request was written in |

---

## 5. Integration requirements

The contract with the rest of the ecosystem. These are the constraints a host
service depends on and cannot verify for itself.

| ID | Requirement |
| --- | --- |
| **IR-01** | The library does not import `net/http`. It protects *a call*, whatever the transport, and an HTTP-shaped API would make the abstraction lie |
| **IR-02** | Hooks are called synchronously and must not block. `bastion` holds no goroutine pool to absorb a slow hook, and a hook that blocks a call blocks the request behind it |
| **IR-03** | No global state: no package-level mutable configuration, no `init()` that registers anything. Two breakers in one process are independent (FR-10) |
| **IR-04** | The library never logs, prints or panics in normal operation. Everything an operator needs surfaces through return values and the hooks of FR-09 |
| **IR-05** | `bastion` composes with [`moat`](https://github.com/JonasBorgesLM/moat) at the host, and does not import it. The two guard opposite directions and share no type |

The first service to plug `bastion` in is the gateway, protecting its reverse
proxy calls to `task-api`: a reverse proxy without resilience is a single point
of failure for everything behind it.

### 5.1 Composing with moat

The gateway runs both, and they meet without touching:

```
inbound request ──▶ moat middleware chain ──▶ gateway handler
                    (rate limit, CSRF,              │
                     headers, validation)           │  outbound
                                                    ▼
                                           bastion-guarded call ──▶ task-api
```

`moat` decides whether a request is allowed in. `bastion` decides whether a call
is allowed out. Nothing crosses: no shared type, no shared configuration, and
**no `require` line in either direction** (NFR-03).

That is a deliberate departure from `cairn`, which does take `moat` as its one
first-party dependency — for `secret.Value`, because a destination URL is
sensitive and must redact itself through every formatting path. `bastion` holds
no secret. A breaker's name, its state and its counters are all safe to print,
so the reason `cairn` pays that cost does not exist here, and taking the
dependency anyway would put `moat` in the `go.sum` of every service that wanted
a circuit breaker.

Two integration points do exist, and both are the host's code rather than
either library's:

| Concern | Where it is handled |
| --- | --- |
| Turning `ErrOpenState` into a response — a `503` with `Retry-After`, never a `500` | The gateway's error mapping. An open circuit is a deliberate, temporary refusal, and a `500` tells the caller to give up when it should tell them to come back |
| A rejected outbound call must not consume the caller's rate-limit budget twice | The gateway's ordering: `moat`'s limiter has already admitted the request; `bastion` rejecting downstream is not a second offence by that client |

Neither belongs in a library. Both belong in the ADR that records where
`bastion` plugs in first.

---

## 6. Architecture decisions

Recorded as ADRs in [`docs/adr/`](docs/adr/README.md) before the code that
depends on them. The shape below is the starting proposal, not a settled
outcome.

- **State pattern** for the Closed / Open / Half-Open machine (FR-01).
- **Functional options** for breaker configuration, matching `moat` and `cairn`.
- **Explicit composition** between retry, timeout and breaker — a decorator each,
  no rigid coupling, so any one of them can be used alone (FR-06, FR-07).
- **Generics** for the main entry point, so a call site is not forced through a
  manual type assertion on `interface{}`.
- **Strategy** for the error-classification function (FR-04).

### 6.1 Proposed package layout

One flat root package, in the shape of `cairn`: one file per concept, tests
alongside, no subpackage until something earns one.

```
bastion/
├── doc.go          package documentation and current status
├── breaker.go      the Breaker type, its construction and its entry point
├── state.go        State, its values and its transitions
├── retry.go        retry with backoff and jitter — composable, never required
├── clock.go        the Clock interface and the system implementation
├── errors.go       ErrOpenState, ErrTooManyRequests and the rest
├── options.go      functional options
└── hooks.go        observability hooks
```

Named `hooks.go` rather than `metrics.go`: FR-09 exists precisely so that no
metrics library is imported, and a file called `metrics.go` invites the opposite.

---

## 7. Development phases

| Phase | Scope |
| --- | --- |
| **B1** | State machine (FR-01) with transition tests on a fake clock (NFR-05) |
| **B2** | Error classification (FR-04) and context cancellation (FR-05) |
| **B3** | Retry with backoff and jitter (FR-06), timeout via context (FR-07) |
| **B4** | Named breakers (FR-10) and functional options |
| **B5** | Fallback (FR-08) and observability hooks (FR-09, IR-02) |
| **B6** | Overhead benchmarks (NFR-02) and concurrency tests (NFR-01) |
| **B7** | Documentation, runnable examples, and the first integration in the gateway |
| **B8** | v2: adaptive percentage threshold (FR-03) |

---

## 8. Open questions

Deferred deliberately and listed rather than left implicit — an undecided
question that looks decided is the one that gets implemented by accident.

**They are listed in [`docs/adr/README.md`](docs/adr/README.md), and only
there.** That file is where they turn into records, so keeping a second copy
here would mean maintaining two lists that answer the same question and
inevitably drift — which they did, within an hour of both being written.

Code comments cite an open question **by name**, never by position. A `§8.3`
resolves to a different question the moment a list is reordered, and no check in
this repository can catch that: `check-docs.sh` verifies requirement ids and
links, not section numbers.

---

## 9. Prior art

For design benchmarking, not for copying an API.

- `sony/gobreaker` — the most cited reference implementation in the Go
  ecosystem; a simple API built around `Execute`.
- `mercari/go-circuitbreaker` — context-aware, and explicit about the
  cancellation case that FR-05 is drawn from.
- More recent libraries in the ecosystem treat an adaptive percentage threshold
  as the evolution of the classic static model, which is where FR-03 comes from.

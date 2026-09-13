# bastion

A Go resilience library for calls between services: a circuit breaker, retry
with exponential backoff and jitter, and timeouts expressed through context — so
that a failure in one component does not become a failure in everything that
depends on it.

**Zero external dependencies** — the standard library only.

> **Status: early development.** The state machine (B1), error classification
> (B2) and retry with backoff and jitter (B3) are implemented and tested —
> `Execute`, the [`Breaker`](breaker.go) type, `StateClosed` / `StateOpen` /
> `StateHalfOpen` and their transitions, `WithIsFailure`,
> context-cancellation accounting, and [`Retry`](retry.go) with
> [`RetryPolicy`](retry.go). Timeout needs no code of its own (see the
> Roadmap). Fallback and everything past B3 in the [Roadmap](#roadmap) is
> not. Breaking changes are still expected before v1.

---

## Why this exists

With a gateway acting as a reverse proxy in front of `task-api`, every call
between components is exposed to infrastructure failure: a timeout, a 5xx, a
refused connection. Without protection, the failure does not stay where it
started. The proxy's goroutines pile up waiting on a dependency that is already
gone, and a service that was merely slow takes down the one in front of it.

The pattern that answers this is forty years old and widely implemented. What
tends to be missing is not the state machine — that part is easy — but the
decisions around it:

- What counts as a failure? A 404 is an answer from a healthy service, and a
  library that counts it opens a circuit on something that is working.
- What happens when *the caller* cancels? Counting a client-side deadline as a
  failure lets a slow client open a circuit; counting it as a success hides a
  dependency that genuinely is not answering.
- Where does retry sit relative to the breaker? Outside, a retried burst can
  open the circuit on its own. Inside, the breaker sees one call where three
  were made.
- What does the breaker report, and to whom, without dragging a metrics library
  into everyone who imports it?

`bastion` takes positions on those and **writes down the reasoning**, so it can
be inspected rather than trusted:

- [`REQUIREMENTS.md`](REQUIREMENTS.md) — the `FR-`/`NFR-`/`IR-` identifiers that
  every commit, test and ADR cites
- [`docs/adr/`](docs/adr/README.md) — the decision record, including the
  questions still open and the phase each one blocks
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — the git flow, the commit convention,
  and what CI will have an opinion about

## Design principles

- **Zero external dependencies.** A resilience library is imported by
  everything, so anything it depends on is depended on by everything. This is
  checked mechanically in CI, not promised in a README.
- **Each piece works alone.** Breaker, retry and timeout compose by explicit
  wrapping at the call site, never by configuration. A reader can see which
  protections are in play without opening a constructor.
- **Time is injected, never slept on.** Every transition is decided against a
  `Clock` the caller can substitute, so the test suite is deterministic and a
  breaker owns no goroutine and no timer.
- **The library never logs, prints or panics.** Errors come back as values;
  everything an operator needs surfaces through hooks the host wires into
  whatever it already runs.
- **No global state.** No package-level configuration, no registry, no `init`.
  Two breakers in one process are two breakers.
- **Fail visibly rather than degrade quietly.** A configuration the library will
  not build from is an error at construction, not a surprising default at the
  first call.

### And what it does not do

Stated here rather than in a footnote, because a list that mentions only wins is
a sales page.

- **It does not make a failing dependency work.** A circuit breaker converts a
  slow failure into a fast one. That is worth a great deal — it is what stops
  the cascade — and it is not the same as availability.
- **It does not survive a restart.** State lives in memory. A process that
  restarts starts every circuit closed, and will rediscover a broken dependency
  the same way it did the first time.
- **It does not coordinate across instances.** Each process has its own view.
  Ten replicas will open their circuits at ten slightly different moments.
- **It does not do routing, transport or metric export.** It does not import
  `net/http`. Those belong to the host,
  [`moat`](https://github.com/JonasBorgesLM/moat) and
  [`crier`](https://github.com/JonasBorgesLM/crier).

## Design shape

One flat package. No subpackage until something earns one.

```
bastion/
├── doc.go        package documentation and current status
├── breaker.go    the Breaker type, its construction and its entry point
├── state.go      State, its values and its transitions
├── retry.go      retry with backoff and jitter — composable, never required
├── clock.go      the Clock interface and the system implementation
├── errors.go     the sentinel errors
├── options.go    functional options
└── hooks.go      observability hooks
```

`hooks.go` rather than `metrics.go`, and the name is the design: the whole point
of that file is that no metrics library is named anywhere in this module.

## Roadmap

| Phase | Scope | |
| --- | --- | --- |
| B1 | State machine, with transition tests on a fake clock | done |
| B2 | Error classification and context cancellation | done |
| B3 | Retry with backoff and jitter; timeout via context | done |
| B4 | Named breakers and functional options — validation still open | partial |
| B5 | Fallback and observability hooks — hooks landed with B1; the fallback needs its own ADR first | partial |
| B6 | Overhead benchmarks and concurrency tests — a smoke test covers `-race` today; the full suite and the benchmarks are still open | partial |
| B7 | Documentation, runnable examples, first integration in the gateway | |
| B8 | v2: adaptive percentage threshold | |

`Retry` composes *around* `Execute`, not inside it — each retry attempt is its
own, individually admitted and counted call:

```go
result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (T, error) {
	return bastion.Execute(ctx, breaker, realOp)
})
```

Requirement-by-requirement detail is in [`REQUIREMENTS.md`](REQUIREMENTS.md), and
the decisions behind B1 through B3's shape — the entry point's exact signature,
the threshold model, panic accounting, stale-probe recovery, context
cancellation, the retry/breaker composition order, and why timeout gets no
helper of its own — are
[ADR-0001](docs/adr/0001-entry-point-is-a-free-generic-function-named-execute.md)
through
[ADR-0007](docs/adr/0007-no-dedicated-timeout-helper.md).
A full API section, with install instructions and a quickstart, is B7's job —
this library still breaks between commits.

## The ecosystem

`bastion` is one of a set of small, independent Go libraries meant to be used
together or apart:

| | |
| --- | --- |
| [`moat`](https://github.com/JonasBorgesLM/moat) | composable HTTP security middleware |
| [`cairn`](https://github.com/JonasBorgesLM/cairn) | URL shortening with security as a requirement |
| [`crier`](https://github.com/JonasBorgesLM/crier) | log and event transport |
| `bastion` | resilience for calls between services |

Each is its own module with its own release cadence. None requires the others.

### With moat, specifically

The gateway runs both, and they meet without touching. `moat` decides whether a
request is allowed **in**; `bastion` decides whether a call is allowed **out**.

```
inbound ──▶ moat chain ──▶ your handler ──▶ bastion-guarded call ──▶ task-api
            (rate limit, CSRF,
             headers, validation)
```

No shared type, no shared configuration, and no `require` line in either
direction. That is a deliberate difference from `cairn`, which *does* depend on
`moat` — for `secret.Value`, because a destination URL is sensitive and has to
redact itself through every formatting path. `bastion` holds no secret: a
breaker's name, its state and its counters are all safe to print. Taking the
dependency anyway would put `moat` in the `go.sum` of every service that wanted
nothing but a circuit breaker.

Two things the *host* has to get right when running both, neither of which is a
library's job:

- **`ErrOpenState` becomes a `503` with `Retry-After`, never a `500`.** An open
  circuit is a deliberate, temporary refusal. A `500` tells the caller to give
  up when it should be telling them to come back.
- **A rejected outbound call is not a second offence by the client.** `moat`'s
  rate limiter already admitted the request; `bastion` refusing downstream must
  not charge that client's budget again.

The full boundary is in [`REQUIREMENTS.md`](REQUIREMENTS.md) §5.1.

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) first — the commit convention and the
documentation rules are enforced by CI, and meeting them for the first time on a
red pull request is nobody's idea of a good afternoon.

## License

MIT. See [`LICENSE`](LICENSE).

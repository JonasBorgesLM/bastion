# bastion

A Go resilience library for calls between services: a circuit breaker, retry
with exponential backoff and jitter, and timeouts expressed through context — so
that a failure in one component does not become a failure in everything that
depends on it.

**Zero external dependencies** — the standard library only.

```go
breaker, err := bastion.New("payments-api", bastion.WithFailureThreshold(5))
if err != nil {
    log.Fatal(err)
}

result, err := bastion.Execute(ctx, breaker, func(ctx context.Context) (Response, error) {
    return client.Call(ctx, request)
})
```

> **Status: pre-v1, not yet tagged.** The full surface documented in
> [Usage](#usage) below — `Execute`, `Retry`, `Fallback`, every option, every
> hook — is implemented and tested at 100% statement coverage
> ([`docs/benchmarks.md`](docs/benchmarks.md) has the overhead numbers). What
> is not done is release engineering: no version has been tagged, and the API
> may still change before one is. See the [Roadmap](#roadmap) for what
> remains.

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

## Install

```sh
go get github.com/JonasBorgesLM/bastion
```

Requires **Go 1.24+** — the floor is the ecosystem's own (matching `moat` and
`cairn`), not a feature this module needs for itself; see the comment in
[`go.mod`](go.mod). No `require` line, and none is expected: the
`boundaries` CI job fails the build the day one appears.

## Usage

The whole surface, grouped by what each piece does. Full contracts are in
each identifier's own godoc; ADRs cited below are where the reasoning for a
non-obvious choice lives.

### Guard a call — `Execute`

```go
result, err := bastion.Execute(ctx, breaker, func(ctx context.Context) (T, error) {
	return realOp(ctx)
})
```

`Execute` is the entry point, generic over the operation's result type
([ADR-0001](docs/adr/0001-entry-point-is-a-free-generic-function-named-execute.md)).
`op`'s own return value and error reach the caller unchanged on every path.
A rejected call returns [`ErrOpenState`](errors.go) or
[`ErrTooManyRequests`](errors.go) without invoking `op` at all. A panic in
`op` is recovered, counted as a failure, and re-raised with its original
value once the breaker's own bookkeeping is done
([ADR-0003](docs/adr/0003-a-panic-always-counts-as-a-failure-and-is-re-raised.md)).
A context the caller cancels counts as neither a success nor a failure
([ADR-0005](docs/adr/0005-context-cancellation-is-detected-by-reading-the-outer-ctx.md)).

### Construct one — `New` and its options

```go
breaker, err := bastion.New("payments-api",
	bastion.WithFailureThreshold(5),   // consecutive failures before Open (default 5)
	bastion.WithOpenTimeout(30*time.Second), // how long Open lasts before a probe (default 30s)
	bastion.WithHalfOpenMaxCalls(1),   // probes admitted per Half-Open window (default 1)
	bastion.WithIsFailure(myClassifier), // decide what counts as a failure (default: any non-nil error)
	bastion.WithHooks(bastion.Hooks{...}),
)
```

`name` is required and appears in every hook event; it is an identifier for
an operator, not a key — `New` registers nothing anywhere, and two breakers
sharing a name are still two independent breakers. Every option above is
validated: a non-positive threshold, timeout, or allowance, or a nil
`Clock`, returns [`ErrInvalidConfig`](errors.go) rather than building a
breaker that would misbehave later. `WithClock` exists for tests — see
[`Clock`](clock.go) — hosts leave it at the default `SystemClock`.

### Retry — `Retry` and `RetryPolicy`

```go
result, err := bastion.Retry(ctx, bastion.RetryPolicy{
	MaxAttempts: 3,
	BaseDelay:   100 * time.Millisecond,
	MaxDelay:    2 * time.Second,
	Jitter:      0.5,
}, func(ctx context.Context) (T, error) {
	return bastion.Execute(ctx, breaker, realOp)
})
```

`Retry` composes *around* `Execute`, never inside it — each attempt is its
own, individually admitted and counted call
([ADR-0006](docs/adr/0006-retry-composes-around-the-breaker-not-inside-it.md)).
An invalid policy (a negative field, or `MaxDelay` below `BaseDelay`, or
`Jitter` outside `[0, 1]`) returns `ErrInvalidConfig` without ever calling
the operation. The wait between attempts is a timer raced against `ctx`,
never `time.Sleep`; a cancelled context returns at once with its own error,
not the previous attempt's.

Composed as shown above, a rejection from the breaker (`ErrOpenState`,
`ErrTooManyRequests`) stops the loop immediately instead of paying for the
remaining backoff schedule first — `RetryPolicy.IsRetriable`'s nil default
excludes exactly those two errors, since a rejected attempt never reached
the dependency
([ADR-0011](docs/adr/0011-retry-skips-the-wait-after-a-breaker-rejection.md)).
Set `IsRetriable` to retry through a rejection anyway, or to exclude other
permanent errors of your own.

### Fallback — `Fallback`

```go
result, err := bastion.Execute(ctx, breaker, realOp)
result, err = bastion.Fallback(ctx, result, err, func(ctx context.Context, err error) (T, error) {
	return cachedOrDefaultValue, nil
})
```

Always called *after* `Execute` returns, never nested inside the operation
it wraps — nesting it would let the fallback's own success register as a
success against the breaker
([ADR-0008](docs/adr/0008-fallback-is-a-post-execute-call-site-function.md)).
Runs on any non-nil `err` — a rejection or a genuine failure, treated
alike — except when the caller's own `ctx` is already cancelled, in which
case `Fallback` does nothing further on their behalf.

### Timeout — no function, just `context.WithTimeout`

```go
ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
defer cancel()
result, err := bastion.Execute(ctx, breaker, realOp)
```

No bastion-specific wrapper exists for this on purpose: `go vet`'s
`lostcancel` analyzer already catches the one mistake this pattern
invites, forgetting `cancel`
([ADR-0007](docs/adr/0007-no-dedicated-timeout-helper.md)).

### Observability — `Hooks`

```go
bastion.WithHooks(bastion.Hooks{
	OnStateChange: func(ctx context.Context, ev bastion.StateChangeEvent) { ... },
	OnCall:        func(ctx context.Context, ev bastion.CallEvent) { ... },
	OnReject:      func(ctx context.Context, ev bastion.RejectEvent) { ... },
})
```

Every field may be `nil`; `nil` is a no-op. Handlers run synchronously, on
the calling goroutine — route them into your own metrics or logging
pipeline asynchronously from there, since a slow handler here blocks the
request behind it.

## Design shape

One flat package. No subpackage until something earns one.

```
bastion/
├── doc.go        package documentation and current status
├── breaker.go    the Breaker type, its construction and its entry point
├── state.go      State, its values and its transitions
├── retry.go      retry with backoff and jitter — composable, never required
├── fallback.go   post-Execute fallback — never nested inside op
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
| B4 | Named breakers and functional options | done |
| B5 | Fallback and observability hooks — hooks landed with B1 | done |
| B6 | Overhead benchmarks and concurrency tests | done |
| B7 | Documentation and examples done; release workflow and the gateway integration (a separate repository) remain | partial |
| B8 | v2: adaptive percentage threshold | |

`Retry` composes *around* `Execute`, not inside it — each retry attempt is its
own, individually admitted and counted call:

```go
result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (T, error) {
	return bastion.Execute(ctx, breaker, realOp)
})
```

`Fallback` composes the same way — strictly after `Execute`, never nested
inside the operation it wraps, since a fallback that ran inside `op` and
succeeded would register as a success against the breaker even though the
real dependency never answered:

```go
result, err := bastion.Execute(ctx, breaker, realOp)
result, err = bastion.Fallback(ctx, result, err, myFallback)
```

Requirement-by-requirement detail is in [`REQUIREMENTS.md`](REQUIREMENTS.md), and
the decisions behind the shapes above — the entry point's exact signature,
the threshold model, panic accounting, stale-probe recovery, context
cancellation, the retry/breaker composition order, why timeout gets no helper
of its own, why fallback runs after `Execute` rather than inside it, and where
`bastion` plugs in first — are every ADR from
[ADR-0001](docs/adr/0001-entry-point-is-a-free-generic-function-named-execute.md)
through
[ADR-0009](docs/adr/0009-bastion-plugs-in-first-at-the-gateway.md).
This library still breaks between commits before a first tagged release.

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

# CLAUDE.md

Guidance for Claude Code when working in this repository.

The general engineering rules — effort proportional to the task, architecture
discipline, clean code, testing, review, security, git hygiene, verification —
are in `~/.claude/CLAUDE.md` and are already loaded. **This file carries only
what is true of bastion**, and where it repeats a global rule it is because this
repository makes it stricter.

## What bastion is

A Go library for resilience between services: a circuit breaker, retry with
exponential backoff and jitter, and timeouts through context. It is **not** an
HTTP client, not a proxy, and it does not import `net/http`.

Requirements live in [`REQUIREMENTS.md`](REQUIREMENTS.md) and are referenced by
id (`FR-01`, `NFR-05`, `IR-02`…). Decisions live in
[`docs/adr/`](docs/adr/README.md).

## Current phase

**B1 and B2 done** — the state machine, `Execute`, observability hooks
(pulled forward from B5; see the note at the end of [`hooks.go`](hooks.go)),
error classification (FR-04) and context-cancellation accounting (FR-05) are
implemented and tested at 100% coverage. B3 onward are not. Work is grouped
B1 through B8 in [`REQUIREMENTS.md`](REQUIREMENTS.md) §7, and each
`TODO(Bn)` in the source names the phase that closes it.

Do not implement a phase whose open question is still open. Four questions
remain in [`docs/adr/README.md`](docs/adr/README.md), blocking B3 through B7;
[ADR-0001](docs/adr/0001-entry-point-is-a-free-generic-function-named-execute.md)
through
[ADR-0005](docs/adr/0005-context-cancellation-is-detected-by-reading-the-outer-ctx.md)
record the five resolved so far.

## Repository layout

**Single module**, one flat package at the root. There is no `internal/`, no
subpackage, and nothing has yet earned one.

```bash
go build ./... && go vet ./... && go test -race ./...
golangci-lint run ./...
./.github/scripts/check-docs.sh
```

There is no Makefile, and that is the ecosystem's convention rather than an
omission: `moat`, `cairn` and `crier` all document plain `go` commands, so the
command you run locally is the command CI runs.

## What CI checks, and why you cannot talk it out of it

A convention CI does not check is documentation, not a convention. Before
writing code, know which jobs will have an opinion:

| Job | Fails when |
| --- | --- |
| `build & test` | build, vet, or `go test -race` fails |
| `boundaries` | `go.mod` gains a require, a `go.sum` appears, or anything reaches `net/http` |
| `lint` | an exported identifier has no doc comment, a `switch` over `State` misses a case, among much else |
| `govulncheck` | a standard-library vulnerability is reachable from this code |
| `docs` | a cited `FR-`/`NFR-`/`IR-` id does not exist, or a relative link is broken |
| `adr-immutability` | an existing ADR loses a line (amend or supersede; never rewrite) |
| `commits` | a commit or the PR title is not a Conventional Commit |
| `CI OK` | any of the above — this is the single required check |

One escape hatch exists and it is visible on the PR: label `adr-typo` for a
genuine typo in an accepted ADR. Fixing the check rather than the code is not
available. If a check is wrong, that is an issue and an ADR, not a
`continue-on-error`.

## Definition of Done

A change is not finished when it compiles. It is finished when:

1. `go build ./...`, `go vet ./...` and `go test -race ./...` pass.
2. `golangci-lint run ./...` passes.
3. `./.github/scripts/check-docs.sh` passes.
4. Any new transition or guard **has been seen to fail** before it was seen to
   pass. Remove the behaviour, watch the test go red, put it back. An assertion
   nobody has watched go red is not verified, it is hoped.
5. The invariants below still hold.

## Non-negotiable invariants

Decided, not open. Each is a defect if violated.

1. **Nothing sleeps, and nothing schedules.** Every time-dependent transition is
   decided by reading the [`Clock`](clock.go) on the next call (NFR-05). A
   `time.Sleep`, a `time.AfterFunc` or a background goroutine in this library is
   a defect: it makes the tests non-deterministic and gives the host a goroutine
   it did not ask for and cannot stop.
2. **A rejected call never runs the operation.** `ErrOpenState` and
   `ErrTooManyRequests` mean the wrapped function was not invoked. A caller that
   cannot rely on that cannot use a breaker in front of anything with a side
   effect.
3. **A cancelled context is neither a success nor a failure** (FR-05). Counting
   it as a failure lets a client-side deadline open a circuit on a healthy
   dependency; counting it as a success hides one that is genuinely not
   answering.
4. **No dependency, and no `net/http`** (NFR-03, IR-01). A resilience library is
   imported by everything, so anything it depends on is depended on by
   everything.
5. **Hooks are synchronous, and a nil hook is a no-op** (IR-02). bastion spawns
   no goroutine per event. A hook that blocks blocks the request behind it, and
   that is documented rather than defended against.
6. **No global state** (IR-03). No package-level mutable configuration, no
   registry of breakers, no `init()`. Two breakers sharing a name are two
   independent breakers, and there is a test that says so.
7. **A configuration the library will not build from is an error, never a
   panic** (IR-04). `New` returns `ErrInvalidConfig`. A library that panics at
   construction takes down a host for something the host could have handled.
8. **The library never logs or prints** (IR-04). Observability is the host's,
   through the hooks.
9. **A panic in the wrapped operation never leaves the breaker locked** (FR-11).
   Every acquisition of `b.mu` around a call to caller-supplied code is released
   by `defer`, without exception. A panic through a held mutex does not surface
   as a panic — it surfaces as every subsequent call to that dependency blocking
   forever, which is the breaker causing the outage it was installed to prevent.
10. **bastion does not import moat** (IR-05). They guard opposite directions and
    compose at the host. `cairn` depends on `moat` for `secret.Value`; bastion
    holds nothing that needs redacting, and taking the dependency would put
    `moat` in the `go.sum` of everyone who wanted only a circuit breaker.

## Conventions

Git hygiene, Conventional Commits and the review discipline are global. These
are bastion's own:

- **English** for all code, comments, documentation and commit messages
  (NFR-08), regardless of the language a request was written in.
- **Commit scopes** are this package's concepts: `breaker`, `state`, `retry`,
  `clock`, `hooks`, `options`, `errors`, `adr`, `docs`, `deps`, `ci`. The
  `commits` CI job rejects anything else.
- **Every structural decision gets an ADR**, written before the code. Do not
  silently resolve a question listed as open in
  [`docs/adr/README.md`](docs/adr/README.md).
- **ADRs are never rewritten.** Amend in place, or supersede with a new one that
  names the old.
- **Go 1.24 is the floor** (NFR-07). Do not raise it for convenience; a
  library's `go` directive is a promise about who may import it.
- **Tests are table-driven**, use `t.Run` subtests, and assert the specific
  behaviour — not just "no error". Standard library only: no testify, no mock
  framework. Test doubles are written by hand in `_test.go` files.
- **Black-box by default**: tests live in `package bastion_test` and use the API
  a consumer would. Reach for `_internal_test.go` only when the thing under test
  genuinely has no observable surface.
- **A transition test drives the fake clock**, never the wall clock. A test that
  sleeps is a test that is slow on your machine and flaky on the runner.

## Writing a transition test

Every transition of FR-01 needs a test with a **negative control**. The pattern:

```go
// FR-01: Open must not admit a call before the timeout has fully elapsed.
// Negative control: verified failing against a breaker using >= rather than >.
```

A test that passes against a broken implementation is worse than no test,
because someone will cite it. Assert the boundary from both sides — one
nanosecond short and exactly on it — since an off-by-one in a comparison is the
defect this catches and it is invisible in review.

And the rule that catches the subtler mistake: **a negative assertion is
satisfied by a call that never happened.** Asserting "the operation returned no
error" passes when the operation was never invoked. Count the invocations.

## Things that go wrong in libraries of this shape

Written down so they are avoided rather than rediscovered:

- **The breaker that counts a 404 as a failure.** A well-formed error response
  from a healthy service opens a circuit on something that is working. That is
  what FR-04's classifier exists for, and why the default has to be documented
  rather than assumed.
- **The retry that synchronises the herd.** Backoff without jitter puts every
  client of a recovering service back on it at the same instant, which is the
  outage the retry was supposed to soften.
- **The half-open state that admits everything.** Without a bound on probe
  calls, the moment the timeout expires the full load arrives at a dependency
  that has just come back, and knocks it over again.
- **The sleep in the retry loop.** `time.Sleep` ignores the context, so a
  cancelled request still waits out the full backoff — and the caller who
  cancelled it has already gone.
- **The state read that does not re-evaluate.** Reporting `Open` for a circuit
  whose timeout has expired is wrong in exactly the place an operator is
  looking: the dashboard.

## Tooling, and where it helps here

Installed globally; this is what is worth reaching for in *this* repository.

- **Graphify** is thin here by construction — one flat package of eight files.
  Read the files. It becomes useful at B7, when the gateway integration makes
  this repository one node in a larger graph.
- **Superpowers' `test-driven-development`** fits the transition work
  particularly well, because a negative control *is* red-green-refactor.
- **`/impeccable`, the animation skills, Playwright and Figma do not apply.**
  bastion has no frontend and will not get one. `.claude/settings.json`
  deliberately enables no design plugin — if you find yourself reaching for one,
  the boundary has been crossed.
- **`claude-code-setup`** is enabled and is advisory. Its recommendations are
  questions, not instructions, and "no, because" is a complete answer.

The general rule that effort is proportional to the task applies with one
exception: **anything touching a state transition or the failure count is a
complex task regardless of its diff size.** A one-character change to a
comparison operator in the timeout check is a one-line diff and a production
incident.

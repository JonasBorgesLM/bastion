# Contributing

Read this before opening a pull request. Everything here is checked by CI, so
meeting it for the first time on a red build is nobody's idea of a good
afternoon.

## The short version

```bash
go build ./... && go vet ./... && go test -race ./...
golangci-lint run ./...
./.github/scripts/check-docs.sh
```

Those three commands are what the pipeline runs. Run them before you push.

## Current phase

bastion is **pre-implementation**: the requirements, the conventions and the
pipeline exist; the domain code does not. Work is grouped B1 through B8 in
[`REQUIREMENTS.md`](REQUIREMENTS.md) §7, and each `TODO(Bn)` in the source names
the phase that closes it.

**Do not implement a phase whose open question is still open.** Seven are listed
in [`docs/adr/README.md`](docs/adr/README.md), each with the phase it blocks.
The decision comes first, as an ADR, then the code. That ordering is not
bureaucracy: a decision embedded in code is one nobody can review, and one of
these — the public entry point's name and signature — cannot be corrected after
release without breaking every caller.

## Branches

Feature branches go to `develop` through a pull request. `main` is the release
branch. Both are protected and require the `CI OK` check.

## Commits

Conventional Commits, enforced by the `commits` job on every commit in the PR
*and* on the PR title, because the title becomes the subject on a squash merge.

```
<type>(<scope>)!: <subject>
```

- **Types:** `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`,
  `chore`, `revert`
- **Scopes:** `breaker`, `state`, `retry`, `fallback`, `clock`, `hooks`,
  `options`, `errors`, `adr`, `docs`, `deps`, `ci`
- Subject: 72 characters or fewer, no trailing period, imperative mood

```
feat(breaker): open the circuit on the Nth consecutive failure
fix(retry): abandon the backoff wait when the context is cancelled
docs(adr): record how context cancellation is accounted for
```

**Explain why in the body** — the diff already says what — and cite the
requirement or decision it serves: `Refs FR-01`, `Closes #12`.

One subject per commit. A commit whose message does not describe everything in
it cannot be reviewed or reverted cleanly.

## Tests

Tests are part of the change, not a follow-up.

- Table-driven, `t.Run` subtests, standard library only. No testify, no mock
  framework. Test doubles are written by hand in `_test.go` files.
- Black-box by default: `package bastion_test`, using the API a consumer has.
- **A transition test drives the fake clock, never the wall clock.** A test that
  sleeps is slow on your machine and flaky on the runner.
- Assert the specific behaviour, not the absence of an error. A negative
  assertion is satisfied by a call that never happened — if you assert that the
  operation did not fail, also count how many times it ran.

### Negative controls

Every state transition and every guard needs a test that **has been seen to
fail**. Remove the behaviour, watch it go red, put it back, and note it above
the test:

```go
// FR-01: Open must not admit a call before the timeout has fully elapsed.
// Negative control: verified failing against a breaker using >= rather than >.
```

A test that passes against a broken implementation is worse than no test,
because someone will cite it. If you cannot make a check fail on demand, say so
in the pull request rather than claiming coverage.

## Documentation

- Every exported identifier has a doc comment stating **the contract**, not the
  signature. The `lint` job fails without one, and several of this library's
  contracts live nowhere else: that hooks are synchronous and must not block,
  that a cancelled context counts as neither outcome, that two breakers sharing
  a name are independent.
- Requirement ids are cited from godoc, tests, ADRs and commit messages. The
  `docs` job fails on a citation that resolves to nothing.
- A structural decision gets an ADR **before** the code, from
  [`docs/adr/TEMPLATE.md`](docs/adr/TEMPLATE.md), indexed in
  [`docs/adr/README.md`](docs/adr/README.md).
- **An ADR is never rewritten.** Amend it in place with a section naming what
  superseded which part, or supersede it with a new record that names the old.
  Editing the original destroys the reasoning that was current when the decision
  was made, which is the only thing it was for. The `adr-immutability` job fails
  when an accepted ADR loses a line; a genuine typo is fixed under the
  `adr-typo` label, visibly.

## Dependencies

There are none, and that is the point (NFR-03). The `boundaries` job fails on a
`require` in `go.mod`, on the appearance of a `go.sum`, and on anything reaching
`net/http` (IR-01).

If you believe a dependency is genuinely needed, that is an ADR and a
conversation, not a commit. The bar is high on purpose: a resilience library is
imported by everything, so anything it depends on is depended on by everything.

## Reviews

Findings are classified **CRITICAL / HIGH / MEDIUM / LOW**, and each states the
evidence: the file, the line, and the concrete path that reaches the problem. A
speculative finding costs more attention than it saves.

Fixing the check rather than the code is not available. If a CI check is wrong,
that is an issue and an ADR — not a `continue-on-error` and not `--no-verify`.

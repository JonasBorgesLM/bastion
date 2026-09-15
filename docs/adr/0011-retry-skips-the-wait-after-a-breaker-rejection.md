# ADR-0011: Retry skips the wait after a breaker rejection, via a caller-overridable retriability predicate

## Status
Accepted

## Context

ADR-0006's own point 2 promised that once the breaker trips, a live retry
loop's subsequent attempts get `ErrOpenState` back "without waiting out the
backoff that would otherwise precede it." That was false as implemented
(issue #47, measured against the published v0.1.0): `Retry`'s loop treats
every non-nil error identically, so a rejected attempt still pays for the
full backoff schedule before discovering, attempt by attempt, that none of
it was buying anything.

A second, related gap sits right next to it (issue #48): `Retry` retries
*every* error, including permanent ones — a `400`, a validation failure —
that waiting and asking again cannot fix. This is asymmetric with the
breaker's own `WithIsFailure`, which exists precisely because "a 404 is an
answer from a healthy dependency." `Retry` had no equivalent.

Both issues are the same question from two directions — "which errors is it
worth spending the retry budget on" — and fixing #47 alone without deciding
#48 would mean revisiting this exact code a second time shortly after,
reconciling whatever hardcoded check closed #47 with whatever general
mechanism closed #48. They are decided together here.

**The coupling question ADR-0006 raised and this ADR has to answer
directly.** ADR-0006 sold "Retry needs no special knowledge of the breaker"
as a virtue of the recommended composition — `Retry` and `Breaker` stay
decoupled; `Retry` doesn't hold a `*Breaker`, doesn't call any of its
methods, doesn't import anything breaker-specific beyond what already lives
in this package. Recognizing `ErrOpenState` and `ErrTooManyRequests` by
value — via `errors.Is`, not by reaching into a `*Breaker` — is a
meaningfully weaker coupling than that: it is `Retry` knowing bastion's own
sentinel vocabulary, the same way `RetryPolicy.validate` already returns
`ErrInvalidConfig`, a sentinel defined for the whole package rather than for
one type in it. No reference to `*Breaker` crosses into `retry.go`; no
method call does either.

## Decision

`RetryPolicy` gains a field:

```go
// IsRetriable reports whether err is worth retrying. nil (the default)
// retries every error except a rejection from a Breaker's Execute --
// ErrOpenState or ErrTooManyRequests -- since retrying immediately after
// either wastes the backoff wait on a call that was never going to reach
// the dependency. Set a non-nil function to retry through a rejection
// anyway, or to exclude other permanent errors (a 400, say) from the
// retry budget as well.
IsRetriable func(error) bool
```

`Retry`'s loop checks it only where the fix actually needs to apply — after
an attempt fails, before deciding to wait and try again, never on the
attempt that was going to be the last one anyway (no wait would have
followed it regardless):

```go
if attempt == maxAttempts {
    break
}
if !retriable(err) {
    return zero, err // stop now: no wait, no further attempt
}
```

The default, when `IsRetriable` is `nil`, is exactly the exclusion this ADR
exists to add: `!errors.Is(err, ErrOpenState) && !errors.Is(err,
ErrTooManyRequests)`. This makes the ADR-0006-recommended composition behave
the way ADR-0006 already told callers it would, without any opt-in — the
same design choice `WithIsFailure`'s own default (count every error) already
made on the breaker side, applied here.

**On coupling:** this is accepted, deliberately, as the weaker form
described above. `Retry` still holds no `*Breaker`, calls no breaker method,
and works identically for a caller who never touches `Breaker` at all — for
such a caller, `ErrOpenState` and `ErrTooManyRequests` simply never occur,
and the default predicate never excludes anything. The coupling is to two
error *values*, not to a type.

## The alternative that was rejected

**Hardcode the `ErrOpenState`/`ErrTooManyRequests` check directly into the
loop, with no `IsRetriable` field.** This was the narrower fix issue #47
sketched on its own. Rejected because it would need revisiting almost
immediately for issue #48's separate, real need (excluding a permanent
`400`) — either as a second hardcoded case that keeps growing, or as a
rewrite into the general mechanism this ADR adopts directly. Shipping the
narrow version first and the general one later means changing
`RetryPolicy`'s shape twice for one underlying question.

**A package-level default predicate a caller can only replace wholesale, not
extend.** Considered and rejected in favor of a caller-supplied
`IsRetriable` being free to call the package's own default internally if
they want to add exclusions on top of it (`bastion` does not currently
export the default function; if a caller needs to compose with it rather
than reimplement the two-sentinel check, that is worth adding on request
rather than speculatively).

## Consequences

- **Behavior changes for every existing caller of `Retry` composed around a
  `Breaker`, without an opt-in.** A rejected attempt now stops the retry loop
  immediately instead of retrying through the full `MaxAttempts`. This is
  pre-1.0 (`RELEASING.md`'s versioning section: the API may change between
  minor versions) and is a bug fix bringing behavior in line with what
  ADR-0006 already documented as the design, not a new feature arriving
  unannounced.
- **A caller who genuinely wants to retry through a rejection** — waiting for
  the circuit to close on its own mid-loop, say — must now say so explicitly
  via `IsRetriable`. This was already awkward to do reliably before this
  ADR (the backoff schedule would need to outlast `openTimeout` by
  construction), so little is actually lost.
- `RetryPolicy.validate` is unaffected — a nil `IsRetriable` is not an error,
  it is the documented default.

## Reopening criterion

Not deferred. This is the decision issues #47 and #48 were both blocked on.

# ADR-0014: Execute gains an empty CallOption slot; Retry and Fallback do not

## Status
Accepted

## Context

```go
func Execute[T any](
	ctx context.Context, b *Breaker, op func(context.Context) (T, error),
) (T, error)

func Retry[T any](
	ctx context.Context, p RetryPolicy, op func(context.Context) (T, error),
) (T, error)

func Fallback[T any](
	ctx context.Context, result T, err error, fb func(context.Context, error) (T, error),
) (T, error)
```

None of the three can gain a parameter for free. Go has no optional
parameters and no overloading; a new parameter changes the signature (issue
#52). `v0.1.0` is published; adoption is still near zero, so this is the
cheapest point at which to decide.

**The issue's own framing needed checking, not assuming — "changing arity
breaks every caller" is not quite true, and the imprecise version matters
less than what actually is true.** Adding a trailing variadic parameter to
an existing exported function does not break an ordinary call site: `go
build` on a caller written as `Execute(ctx, b, op)` still compiles against
`Execute(ctx, b, op, opts ...CallOption)` unchanged, because a variadic
parameter is satisfiable with zero arguments. Verified directly: a two-line
reproduction with an old and a new signature, and a caller using each
compiled fine for the ordinary call, and only failed when the function was
assigned to an explicitly-typed variable (`var fn func(int, func()(int,
error)) (int, error) = pkg.Execute` — a narrow pattern: storing a bastion
entry point as a typed value, for dispatch tables or dependency injection,
rather than calling it directly).

**What does break it, and is what actually matters here, is this
repository's own tooling.** `apidiff` — what `gorelease` uses, and what
`release.yml` runs on every tag — classifies a trailing variadic addition as
an **incompatible change**, not a compatible one, regardless of the
source-level nuance above. Verified directly: `apidiff` between a two-line
package before and after adding `opts ...CallOption` to an exported func
reports:

```
Incompatible changes:
- Execute: changed from func(int) int to func(int, ...CallOption) int
Compatible changes:
- CallOption: added
```

So within this project's own process — the one `RELEASING.md` actually
enforces — adding this parameter later is not a free, silent change; it is a
breaking one requiring a minor bump pre-1.0 (cheap, and exactly what
pre-1.0 is for) or a major bump post-1.0 (expensive, and the reason this
issue is time-sensitive). That is the real cost being weighed, not the
issue's original, slightly overstated framing — and it is a real cost,
confirmed against this repository's own gate rather than assumed.

**The counter-argument the issue itself raises deserves the real hearing it
asks for.** ADR-0007 rejected a timeout helper on "do not introduce
abstractions the current problem does not require." The distinction that
matters: ADR-0007's rejected helper would have been *code that does
something* — a wrapper duplicating three lines `go vet` already covers.
An empty `CallOption` slot is not that. It does nothing, calls nothing,
decides nothing; it is a widening of a type signature with zero
implementation weight, spent once, specifically to keep a *future* addition
inside apidiff's "compatible" column instead of its "incompatible" one. That
is a narrower, cheaper thing than the pattern ADR-0007 declined, and the two
are not the same shape of decision even though they cite the same rule.

**Retry does not have this problem, and this is provable from this
repository's own recent history, not merely argued.** `RetryPolicy` is
already the per-call configuration vessel `Execute` lacks — a fresh value on
every call, not a long-lived shared object like `*Breaker` — and it has
already been extended twice, compatibly, with zero change to `Retry`'s own
signature: `IsRetriable` (ADR-0011) and the elapsed-time-budget
documentation (ADR-0012) both landed as `RetryPolicy` additions. Every "per-call
concern" the issue lists for `Retry` specifically is already expressible as a
future `RetryPolicy` field, the same way those two were. Adding a
`CallOption` slot to `Retry` as well would be solving an already-solved
problem for the sake of symmetry with `Execute`.

**Fallback does not have it either, for a different reason.** It fires no
hooks and touches no breaker state (ADR-0008) — it is a plain post-`Execute`
call-site function. The concerns the issue names (a per-call label for hook
events, a per-call classifier interacting with breaker state) do not attach
to something that observes neither. A future concern specific to `Fallback`
alone (a per-call something that genuinely cannot be the `fb` closure's own
job) would be new evidence; none exists today.

`Execute` is the one entry point actually shaped like the problem: `*Breaker`
is long-lived and shared across every call, so nothing about a single call
has anywhere to land except a new parameter — and unlike `Retry`'s policy,
there is no already-fresh-per-call struct sitting in `Execute`'s parameter
list to grow instead.

## Decision

`Execute` gains a variadic, currently-empty option slot:

```go
// callOptions accumulates CallOption values for a single call to Execute.
// Deliberately empty -- no option is defined yet (ADR-0014). It exists so a
// future option is a compatible addition: a new field here and a new
// WithXxx constructor, never a change to Execute's own signature.
type callOptions struct{}

// CallOption customizes a single call to [Execute]. No option is defined
// yet; see ADR-0014 for why the slot exists anyway. callOptions is
// unexported, so no caller outside this package can construct a non-nil
// CallOption today -- passing none is the only thing to do with this
// parameter until a WithXxx constructor is added.
type CallOption func(*callOptions)
```

```go
func Execute[T any](
	ctx context.Context, b *Breaker, op func(context.Context) (T, error),
	opts ...CallOption,
) (T, error) {
	var o callOptions
	for _, opt := range opts {
		opt(&o)
	}
	// ... unchanged from here
}
```

`Retry` and `Fallback` keep their current signatures. Nothing about this ADR
implies they never gain one — see Reopening criterion.

## The alternative that was rejected

**Add the same slot to all three entry points, for consistency.** Rejected
per the per-function reasoning above: `Retry` already has an equivalent
mechanism (`RetryPolicy`) that has already been used twice; `Fallback`
touches nothing a per-call option would customize today. Adding an unused
slot to either would be exactly the speculative generality the general
engineering rules warn against — the distinction this ADR draws for
`Execute` (a widening with zero weight, spent to stay inside `apidiff`'s
compatible column) does not rescue a slot with no plausible near-term
occupant.

**Do nothing; accept a new function name for each future per-call concern on
`Execute`.** Rejected on the evidence above: this repository's own
`apidiff`/`gorelease` gate makes the "add it later" path strictly more
expensive with every release, in a way this project's own process actually
enforces, not merely a general Go-ecosystem concern. Pre-1.0, right after a
near-zero-adoption first release, is the one point where paying for it is
nearly free.

## Consequences

- `Execute`'s public signature changes (an `apidiff`-incompatible change,
  correctly reported as such): a new minor version, with a changelog entry
  and, per `RELEASING.md`, a note that it is source-compatible for every
  ordinary call site and only affects code that stores `Execute` as an
  explicitly-typed function value.
- No behavior changes for any existing caller who passes no options — proven
  by the entire existing test suite continuing to pass unmodified, since
  every existing call site already omits the new trailing parameter.
- `callOptions` stays unexported until a real option is adopted. Adding the
  first one is an ordinary compatible change from here: a new field, a new
  `WithXxx` constructor, no further ADR needed for the mechanism itself
  (though the option's own behavior may still warrant one, same as any other
  feature).

## Reopening criterion

For `Retry` or `Fallback`: a concrete per-call concern surfaces for either
that cannot be expressed as a `RetryPolicy` field (for `Retry`) or inside the
`fb` closure itself (for `Fallback`). Today, none has.

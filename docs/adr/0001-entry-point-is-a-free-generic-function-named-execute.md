# ADR-0001: The entry point is a free generic function named Execute

## Status
Accepted

## Context

FR-01 needs one call that runs an operation through the breaker and returns
whatever that operation returns. Three things are genuinely open: the name
(`Execute` against `Do` against `Run`), whether it is a method or a free
function, and the exact signature — and this is the one decision in the whole
library that cannot be corrected after release without breaking every caller.

**Method versus free function is not a style choice; it is forced.** Go does
not allow a method to introduce a type parameter that its receiver does not
already have. `Breaker` is not generic — it cannot be, because one named
breaker must guard a dependency called from several call sites with several
different return types (FR-10) — so a hypothetical `func (b *Breaker)
Execute[T any]` with arguments after it does not typecheck. The only way to get a generic result without forcing every
call site through a manual `interface{}` assertion is a free function that
takes the breaker as an argument.

## Decision

```go
func Execute[T any](
    ctx context.Context,
    b *Breaker,
    op func(context.Context) (T, error),
) (T, error)
```

Named `Execute`. It is the name `sony/gobreaker` uses, it is the most cited
reference implementation in the Go ecosystem (REQUIREMENTS.md §9), and a
Go programmer coming from that library reads it correctly on sight. `Do` is
shorter but generic to the point of meaninglessness — every `sync.Once`,
`errgroup` and HTTP client in the standard library and beyond has a `Do`, and
none of them share this one's contract. `Run` reads as fire-and-forget; this
returns a value the caller needs.

`ctx` first, per NFR-04. `b *Breaker` second rather than making `Execute` a
method's sibling on some other type, because the breaker being an explicit,
visible argument is what makes `grep Execute` find every guarded call site in
a codebase — the same reason retry and timeout compose by explicit wrapping
rather than configuration (REQUIREMENTS.md §6).

`op func(context.Context) (T, error)` takes a context so a future timeout
wrapper (FR-07) can derive a deadline and hand it to the operation, and so the
operation itself can react to cancellation without a second channel.

The call's own return value and error pass through unchanged on every path
that actually invokes `op`. `Execute` does not wrap, retype, or annotate the
error — a caller doing `errors.Is(err, sql.ErrNoRows)` on the far side of a
breaker must keep working exactly as it did without one.

## The alternative that was rejected

**A method on `Breaker` with the type parameter on the method.** Rejected
above — it does not compile, because a method cannot introduce a type
parameter its receiver lacks. Documented here anyway because it is the first
thing anyone experienced with generics reaches for, and the reason it fails is
not obvious from the error message alone.

**A non-generic `Breaker.Call(ctx, func(context.Context) error) error`,
returning nothing.** This is `sony/gobreaker`'s original shape before it added
`Execute`. It sidesteps generics entirely, at the cost of forcing every caller
to smuggle a result out through a closure variable:

```go
var result MyType
err := b.Call(ctx, func(ctx context.Context) error {
    var err error
    result, err = fetchMyType(ctx)
    return err
})
```

That pattern compiles and works, and it is exactly the manual
`interface{}`-adjacent boilerplate NFR-04 and REQUIREMENTS.md §6 exist to
avoid. Go has had generics since 1.18; a library started after that point
declining to use them for its own entry point needs a better reason than
inertia.

## Consequences

- Every call site names both the breaker and the operation explicitly, which
  is more to type than a method call but is also greppable and cannot silently
  drift onto the wrong breaker the way a method call embedded deep in a
  struct's own logic can.
- `Execute` is a package-level identifier, so `bastion.Execute` is what every
  caller writes. There is exactly one of it; a second generic entry point
  (for example, a `void`-shaped variant for calls with no return value) would
  need its own name and its own ADR rather than an overload, since Go has
  none.
- The signature is now load-bearing API. Widening it later — adding an
  optional parameter — is possible without breaking callers only via a new
  function, never by changing this one's arity.

## Reopening criterion

Not deferred. This is the decision B1 was blocked on.

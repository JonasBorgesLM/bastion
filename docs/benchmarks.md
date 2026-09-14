# Benchmarks

Issue #28. `go test -bench` output, Apple M1, `go1.27.1`, recorded
2026-09-13. Re-run with `go test . -bench=. -benchmem -run '^$'` to
reproduce; absolute numbers will vary by machine — what matters is the
*shape*: the delta between the baseline and each breaker path, and that
neither path allocates.

## The three paths

```
BenchmarkExecute_ClosedHappyPath-8     11476940    103.6 ns/op    0 B/op   0 allocs/op
BenchmarkExecute_OpenRejectionPath-8   41576498     28.77 ns/op   0 B/op   0 allocs/op
BenchmarkOpDirect-8                   546338931      2.193 ns/op  0 B/op   0 allocs/op
```

`BenchmarkOpDirect` is the floor: calling the benchmark's own trivial
operation with no breaker in front of it at all — roughly 2ns, the cost of
one function call the compiler cannot inline away because it goes through
an interface-shaped `func(context.Context) (int, error)` value.

`BenchmarkExecute_ClosedHappyPath` is what `Execute` costs on top of that
floor when the circuit is Closed and every call succeeds: `admit`'s
mutex-protected state check, the call itself, `complete`'s mutex-protected
bookkeeping, two hook checks (both nil in this benchmark, so each is a
single pointer comparison) — about 100ns total, roughly 50x the floor. In
absolute terms this is still far below the cost of anything the breaker is
actually meant to guard: a network call to a real dependency measures in
the hundreds of *microseconds* at best, so this overhead does not show up
in any real deployment's latency budget.

`BenchmarkExecute_OpenRejectionPath` is the fast-fail path: the operation
is never invoked at all, so this measures `admit`'s rejection branch alone
— under 30ns, faster than the Closed path precisely because it skips the
`op` call and `complete`'s work entirely. This is the number that matters
most for the breaker's actual purpose: an open circuit adds a negligible
~29ns to reject a call, instead of paying whatever the real dependency's
failure mode costs (a connection timeout, commonly measured in seconds).

**Zero allocations on every path.** `Execute` does not allocate — `admit`,
`complete` and the hook dispatch all operate on the `Breaker`'s own fields
under its existing mutex, and `callSafely`'s named returns avoid boxing the
result. A resilience library sitting in front of every guarded call is
exactly the place where an allocation per call would compound; this
benchmark is the check that it does not.

## What to compare a later change against

Rerun this file's three benchmarks and compare `ns/op` and `allocs/op`
against the numbers above. `allocs/op` going from 0 to anything non-zero on
either `Execute` benchmark is the more important signal — a hot-path
allocation is a regression worth blocking on, independent of what a
`benchstat` comparison says about the absolute timing, which is expected to
drift with the machine that runs it.

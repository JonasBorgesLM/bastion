# Benchmarks

Issue #28 (serial numbers) and issue #55 (parallel numbers, added
2026-09-15, ADR-0016). `go test -bench` output, Apple M1, `go1.27.1`.
Re-run with `go test . -bench=. -benchmem -run '^$' -cpu 1,2,4,8` to
reproduce; absolute numbers will vary by machine — what matters is the
*shape*: the delta between the baseline and each breaker path, that neither
path allocates, and how each path's cost changes as core count rises.

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

## The same two paths, under real contention

```
BenchmarkExecute_ClosedHappyPathParallel       111.0 ns/op   (1 core)
BenchmarkExecute_ClosedHappyPathParallel-2     104.1 ns/op   (2 cores)
BenchmarkExecute_ClosedHappyPathParallel-4     150.1 ns/op   (4 cores)
BenchmarkExecute_ClosedHappyPathParallel-8     218.5 ns/op   (8 cores)  <- 2.0x slower than 1 core

BenchmarkExecute_OpenRejectionPathParallel      34.6 ns/op   (1 core)
BenchmarkExecute_OpenRejectionPathParallel-2    65.6 ns/op   (2 cores)
BenchmarkExecute_OpenRejectionPathParallel-4    98.7 ns/op   (4 cores)
BenchmarkExecute_OpenRejectionPathParallel-8   117.9 ns/op   (8 cores)  <- 3.4x slower than 1 core
```

Every `Execute` takes `b.mu` twice, and it is shared by every goroutine
calling through one `Breaker`. `b.RunParallel` is what actually exercises
that: the plain benchmarks above run one goroutine regardless of `-cpu`, so
they report a flat number across core counts and cannot show contention at
all — this is why both shapes exist in this file, not just the parallel
one. Adding cores costs throughput per call here, on both paths, worse in
relative terms on the rejection path. That signature — throughput falling as
concurrency rises — is lock contention plus cache-line traffic on the shared
`Breaker`, not the cost of the work itself, which allocates nothing and is a
handful of comparisons.

**Read this number as a worst case, not a representative one.** `b.mu` is
held only for `admit` and `complete`'s own bookkeeping, never while `op`
runs (FR-11) — the benchmark's `op` returns instantly, so every goroutine
spends essentially all its time either inside the ~100–200ns critical
section or waiting for it, with nothing spacing acquisitions apart. A real
`op` is the dependency call this library exists to guard: microseconds at
best, commonly milliseconds, during which `b.mu` sits idle for other
goroutines to use. Even at this benchmark's adversarial 8-core number, the
absolute cost — ~220ns for the happy path, ~120ns for a rejection — remains
three to four orders of magnitude below what any real dependency call costs.
This is why [ADR-0016](adr/0016-the-single-mutex-throughput-ceiling-is-accepted-not-optimized.md)
accepts the ceiling rather than spending the state machine's single-critical-
section correctness guarantee to move it.

## What to compare a later change against

Rerun this file's benchmarks and compare `ns/op` and `allocs/op` against the
numbers above, across `-cpu 1,2,4,8` for the two `Parallel` benchmarks.
`allocs/op` going from 0 to anything non-zero on any `Execute` benchmark is
the more important signal — a hot-path allocation is a regression worth
blocking on, independent of what a `benchstat` comparison says about the
absolute timing, which is expected to drift with the machine that runs it. A
parallel number moving further from its serial counterpart than the ratios
above is worth investigating before assuming it is only machine noise.

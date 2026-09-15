# ADR-0016: The single-mutex throughput ceiling is measured and accepted, not optimized away

## Status
Accepted

## Context

`Execute` takes `b.mu` twice per call — once in `admit`, once in `complete`
— and that mutex is shared by every goroutine calling through one `Breaker`.
Issue #55 measured the consequence directly: adding cores does not add
throughput, it costs it.

Reproduced independently here, Apple M1, `go1.27.1`, `go test -bench=. -cpu
1,2,4,8`:

```
BenchmarkExecute_ClosedHappyPathParallel      111.8 ns/op   (1 core)
BenchmarkExecute_ClosedHappyPathParallel-2    104.6 ns/op   (2 cores)
BenchmarkExecute_ClosedHappyPathParallel-4    151.0 ns/op   (4 cores)
BenchmarkExecute_ClosedHappyPathParallel-8    214.8 ns/op   (8 cores)  <- 1.9x slower than 1 core

BenchmarkExecute_OpenRejectionPathParallel     34.6 ns/op   (1 core)
BenchmarkExecute_OpenRejectionPathParallel-2   65.6 ns/op   (2 cores)
BenchmarkExecute_OpenRejectionPathParallel-4   98.4 ns/op   (4 cores)
BenchmarkExecute_OpenRejectionPathParallel-8  118.2 ns/op   (8 cores)  <- 3.4x slower than 1 core
```

The signature is real: throughput per core falls as core count rises, on
both paths, worse on the rejection path in relative terms. This is not in
question. What issue #55 leaves open, deliberately, is what to do about it
— "accept it and document the ceiling honestly with a number" is named
explicitly as a legitimate outcome, not a fallback for lacking a better idea.

**What the synthetic benchmark cannot show, and has to be stated rather than
left implicit: it is close to the worst case a real deployment produces, not
a representative one.** `admit` and `complete` are the only sections `b.mu`
guards; `op` itself runs with the mutex fully released (unconditionally, so
a panicking or hanging `op` can never hold it — FR-11, and stated already on
`Execute`'s own godoc). The benchmark's `op` returns instantly, so every
goroutine spends effectively 100% of its time either running the ~100ns
critical sections or waiting on `b.mu` — nothing spaces the lock
acquisitions out. A real `op` is a call to the dependency this library
exists to guard: microseconds at the very fastest, commonly milliseconds,
and by design the mutex is not held for any of it. Real contention on `b.mu`
is bounded by how often *admission decisions*, not real work, happen
concentrated in time — and the absolute cost even at 8-core worst-case
contention, ~215ns, remains three to four orders of magnitude below what the
dependency call it wraps costs. The benchmark is honest about the ceiling;
it is not evidence that a real deployment hits it.

**The optimization directions available carry a cost this project's own
priorities weigh against them.** Both directions issue #55 names — atomics
for the Closed fast path, sharded counters — trade the single critical
section's defining property, stated on `admit`'s own doc comment, for
throughput: "one comparison, read from two call sites, cannot drift from
itself." An atomic fast path admitting a call without taking `b.mu` creates
exactly the race the mutex exists to prevent: a call admitted on a stale
read of `state` the instant another goroutine's failure crosses
`FailureThreshold`, silently weakening FR-02's exact-count guarantee for a
number that, per the measurement above, does not matter in any deployment
this library's own stated use case describes. Sharded counters carry the
same problem one level down, and — per #55's own text — complicate
`Counts()` (ADR-0015), which was deliberately kept to "one lock, one
consistent snapshot" days before this ADR was written.

## Decision

**Accept the ceiling. Do not add atomics or sharded counters.** The single
critical section stays exactly as it is. `docs/benchmarks.md` gains the
parallel numbers alongside the existing serial ones, with the interpretation
above stated plainly — a benchmark file that reports only the flattering,
uncontended case is measuring the case that does not happen in production,
and issue #55 is right that the omission itself, not the number, was the
actual defect.

## The alternative that was rejected

**Atomics for the Closed-state fast path.** Rejected: the correctness this
buys back is speed on a critical section that is already three to four
orders of magnitude cheaper than what it guards, paid for by reintroducing
exactly the kind of state-machine race this library's whole design
discipline exists to rule out. `admit`/`complete`'s single critical section
is not an implementation detail; every other ADR in this project's history
that touches the state machine (0003, 0004, 0005, 0010) treats correctness
under concurrency as the non-negotiable property and performance as
secondary, matching this project's own stated priority order (correctness
before performance, "when it is relevant"). Nothing about this measurement
makes it relevant enough to spend that.

**Sharded counters.** Rejected on the same correctness-versus-marginal-gain
basis, plus the concrete complication ADR-0015 already avoided for
`Counts()`: reading a consistent view back out of shards is a second
synchronization problem, introduced to solve a bottleneck real op latency
already dwarfs.

## Consequences

- The measured ceiling is now a permanent, documented number future changes
  are compared against, not an unmeasured assumption.
- A future consumer whose real, measured workload genuinely bottlenecks here
  — many cores, a dependency call cheap enough to be comparable to `admit`'s
  own cost, which is not the shape this library's stated use case describes
  — is new evidence and reopens this decision with a number of their own,
  not a theoretical objection to the one already on record.

## Reopening criterion

A real, measured workload where `b.mu` contention is shown to be the actual
throughput bottleneck — not a synthetic zero-cost-`op` benchmark, a
deployment where the guarded call itself is cheap enough that lock
contention is comparable to it. Today, none has been reported.

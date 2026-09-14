// Package bastion protects service-to-service calls against cascading failure:
// a circuit breaker, retry with exponential backoff and jitter, and timeouts
// expressed through context.
//
// # Status
//
// Pre-v1, not yet tagged. The full API — [Execute], the hooks that report
// it, error classification via [WithIsFailure], context-cancellation
// accounting, [Retry] with [RetryPolicy], every [Option] rejecting a
// configuration [New] will not build a breaker from, and [Fallback] — is
// implemented and tested at 100% statement coverage, with overhead measured
// in docs/benchmarks.md. What remains before a v0.1.0 tag is release
// engineering, not code: see REQUIREMENTS.md's phase table and RELEASING.md.
// Every identifier below may still change before that tag.
//
// # Scope
//
// This package owns the circuit state machine, the classification of an error
// as a failure, and retry scheduling. A per-operation timeout is
// context.WithTimeout, used directly at the call site rather than through a
// bastion wrapper — there is no timeout type or function here on purpose
// (docs/adr/0007-no-dedicated-timeout-helper.md). This package does not own
// transport, routing, metric export or log shipping, and it does not import
// net/http (IR-01): what it protects is a call, whatever carries it.
//
// # Conventions
//
// Each of retry, timeout and breaker is usable alone. They compose by explicit
// wrapping at the call site rather than by configuration, so a reader can see
// which protections are in play without consulting a constructor.
//
// The library never logs, prints, or originates a panic of its own in normal
// operation (IR-04). Errors are returned as values, comparable with
// errors.Is, and everything an operator needs surfaces through the hooks in
// [Hooks] — called synchronously, so a hook that blocks blocks the request
// behind it (IR-02). [Execute] does re-raise a panic that the wrapped
// operation itself raised, unchanged, once its own bookkeeping is complete —
// that is propagation, not origination; see Execute's own doc comment and
// docs/adr/0003-a-panic-always-counts-as-a-failure-and-is-re-raised.md. The
// same is true of a panic in a hook: it can never corrupt bastion's own
// bookkeeping or prevent op from running, and it is re-raised to Execute's
// caller once that bookkeeping is resolved — see
// docs/adr/0010-a-hook-panic-never-corrupts-bookkeeping.md.
//
// There is no global state (IR-03). A Breaker's configuration is fixed once
// [New] returns, and two breakers in one process share nothing.
//
// See REQUIREMENTS.md and docs/adr/ in the repository.
package bastion

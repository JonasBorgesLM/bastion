// Package bastion protects service-to-service calls against cascading failure:
// a circuit breaker, retry with exponential backoff and jitter, and timeouts
// expressed through context.
//
// # Status
//
// Early development. B1 through B4 — the state machine, [Execute], the
// hooks that report it, error classification via [WithIsFailure],
// context-cancellation accounting, [Retry] with [RetryPolicy], and every
// [Option] rejecting a configuration [New] will not build a breaker from —
// are implemented and tested. Everything past B4 in REQUIREMENTS.md's phase
// table is not, and every identifier below may still change before v1.
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
// docs/adr/0003-a-panic-always-counts-as-a-failure-and-is-re-raised.md.
//
// There is no global state (IR-03). A Breaker's configuration is fixed once
// [New] returns, and two breakers in one process share nothing.
//
// See REQUIREMENTS.md and docs/adr/ in the repository.
package bastion

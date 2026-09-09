// Package bastion protects service-to-service calls against cascading failure:
// a circuit breaker, retry with exponential backoff and jitter, and timeouts
// expressed through context.
//
// # Status
//
// Pre-implementation. This package declares its shape and nothing else: the
// requirements, the decision records and the pipeline that enforces them are
// the deliverable of the current phase, and the domain code follows the phases
// in REQUIREMENTS.md. Nothing here is usable yet, and every identifier below
// may still change.
//
// # Scope
//
// This package owns the circuit state machine, the classification of an error
// as a failure, retry scheduling and the timeout wrapper. It does not own
// transport, routing, metric export or log shipping, and it does not import
// net/http (IR-01): what it protects is a call, whatever carries it.
//
// # Conventions
//
// Each of retry, timeout and breaker is usable alone. They compose by explicit
// wrapping at the call site rather than by configuration, so a reader can see
// which protections are in play without consulting a constructor.
//
// The library never logs, prints or panics in normal operation (IR-04). Errors
// are returned as values, comparable with errors.Is, and everything an operator
// needs surfaces through the hooks in [Hooks] — called synchronously, so a hook
// that blocks blocks the request behind it (IR-02).
//
// There is no global state (IR-03). A Breaker's configuration is fixed once
// [New] returns, and two breakers in one process share nothing.
//
// See REQUIREMENTS.md and docs/adr/ in the repository.
package bastion

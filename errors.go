package bastion

import "errors"

// Sentinel errors, all comparable with errors.Is (NFR-04).
//
// They are distinct on purpose. A caller that cannot tell "the circuit refused
// this call" from "the remote dependency failed" cannot report the difference
// to an operator either, and those two have opposite remedies.
var (
	// ErrOpenState reports that the circuit is open and the call was never
	// attempted. It is not a failure of the remote dependency and must not be
	// counted as one (FR-01).
	ErrOpenState = errors.New("bastion: circuit is open")

	// ErrTooManyRequests reports that the circuit is half-open and has already
	// admitted its allowance of probe calls. Like ErrOpenState, the wrapped
	// operation was never invoked.
	ErrTooManyRequests = errors.New("bastion: too many requests in half-open state")

	// ErrInvalidConfig reports that a configuration value this library was
	// given does not describe anything it can act on: [New]'s options, or a
	// [RetryPolicy] passed to [Retry]. Returned rather than panicked: a
	// library that panics on configuration takes down a host at startup, or
	// mid-request, for something the host could have handled (IR-04).
	ErrInvalidConfig = errors.New("bastion: invalid configuration")

	// ErrGroupFull reports that a [Group] already holds MaxKeys breakers and
	// [Group.Get] was asked for a key it has not seen before. Returned as a
	// hard, visible failure rather than silently evicting an existing
	// member's accumulated evidence (ADR-0019) — call [Group.Delete] to free
	// a key deliberately, or [Group.Len] to watch how close the group is to
	// its cap before this happens.
	ErrGroupFull = errors.New("bastion: group is full")
)

// A cancelled context is accounted for as neither a success nor a failure
// (FR-05, ADR-0005) in [Execute], not by an error here: there is no sentinel
// for it, because op's own error — including op's own context.Canceled or
// context.DeadlineExceeded, if it returns one — always reaches the caller
// exactly as op returned it. Cancellation changes only what the breaker does
// with the outcome internally.

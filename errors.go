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

	// ErrInvalidConfig reports that New was given a configuration it will not
	// build a Breaker from. Returned rather than panicked: a library that
	// panics on configuration takes down a host at startup for something the
	// host could have handled (IR-04).
	ErrInvalidConfig = errors.New("bastion: invalid configuration")
)

// A cancelled context is accounted for as neither a success nor a failure
// (FR-05, ADR-0005) in [Execute], not by an error here: there is no sentinel
// for it, because op's own error — including op's own context.Canceled or
// context.DeadlineExceeded, if it returns one — always reaches the caller
// exactly as op returned it. Cancellation changes only what the breaker does
// with the outcome internally.

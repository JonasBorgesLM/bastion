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

// TODO(B2): the accounting of context cancellation (FR-05).
//
// A cancelled context is neither a success nor a failure of the remote service:
// counting it as a failure lets a client-side deadline open a circuit that
// nothing is wrong behind, and counting it as a success hides a dependency that
// is genuinely slow. The open question is on the record as "how context
// cancellation is accounted for" in docs/adr/README.md, and is decided before
// this is implemented.

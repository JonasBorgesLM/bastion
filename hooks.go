package bastion

import "context"

// Hooks receive lifecycle events (FR-09). Every field may be nil; nil is a
// no-op.
//
// Handlers are called synchronously and must not block (IR-02): bastion holds
// no goroutine pool to absorb a slow hook, and a hook that blocks a call blocks
// the request behind it. Route these into a proper observability pipeline —
// asynchronously, from the host — rather than doing slow work here.
//
// This is the whole of the library's observability surface, and it is
// deliberately made of plain functions and plain structs. Nothing here names a
// metrics library, so a host wires bastion to whatever it already uses without
// bastion appearing in anyone's dependency graph as a reason to adopt one
// (NFR-03).
type Hooks struct {
	// OnStateChange fires after the circuit changes state (FR-01).
	OnStateChange func(ctx context.Context, ev StateChangeEvent)

	// OnCall fires after a call the breaker admitted has completed, whether
	// it succeeded or failed.
	OnCall func(ctx context.Context, ev CallEvent)

	// OnReject fires when the breaker refuses a call without invoking it,
	// because the circuit is open or has exhausted its half-open allowance.
	OnReject func(ctx context.Context, ev RejectEvent)
}

// StateChangeEvent describes one transition of the state machine. From and To
// are always different: a breaker that re-evaluates its state and stays put
// does not emit an event.
type StateChangeEvent struct {
	// Name identifies the breaker (FR-10), so a single handler can serve
	// every breaker in a process.
	Name string

	From State
	To   State
}

// CallEvent describes a call the breaker admitted and that has now finished.
type CallEvent struct {
	Name string

	// State is the circuit's state at the moment the call was admitted, not
	// the state it may have moved to as a result.
	State State

	// Err is the error the operation returned, nil on success. It is the raw
	// error, before classification (FR-04) — a handler that wants to know how
	// the breaker counted it should read Counted.
	Err error

	// Counted reports whether this call moved the failure counters. An error
	// the classifier excused, and a context the caller cancelled, both arrive
	// here with Counted false (FR-04, FR-05).
	Counted bool
}

// RejectEvent describes a call the breaker refused to make. Reason is
// [ErrOpenState] or [ErrTooManyRequests].
type RejectEvent struct {
	Name   string
	State  State
	Reason error
}

// TODO(B5): the call sites. Every hook is invoked from exactly one place, and
// each needs a test asserting it fires with the right event — including the
// nil-handler case, which must not panic.

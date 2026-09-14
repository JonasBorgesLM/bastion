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
// A handler that panics cannot corrupt bastion's own bookkeeping, and cannot
// prevent the guarded operation from running — the panic is recovered,
// bastion's own accounting for the call completes normally, and only then is
// the panic re-raised to the caller of [Execute], visible exactly where an
// operation's own panic already is. See
// docs/adr/0010-a-hook-panic-never-corrupts-bookkeeping.md for the full
// reasoning, including how a handler calling runtime.Goexit is handled.
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

// The call sites are in breaker.go's Execute: OnStateChange from
// fireStateChange, called after admission and again after completion, each
// only when a transition actually happened; OnCall and OnReject inline,
// each from exactly one place. Every one is covered in breaker_test.go,
// including the nil-handler case.
//
// Landed with B1 rather than B5 as REQUIREMENTS.md's phase table originally
// grouped it: firing these three events *is* reporting the state machine's
// own transitions and outcomes, so writing Execute without them meant either
// leaving OnStateChange's own doc comment false, or rewriting the same call
// sites a second time later for no reason. FR-08's fallback is the part of B5
// that is still genuinely deferred — it needs the ADR in
// docs/adr/README.md first, unlike this.

package bastion

// State is the circuit's position in the machine of FR-01.
//
// The zero value is [StateClosed], which is deliberate: a Breaker that was
// never opened, and a process that has just restarted, both start out passing
// calls through. There is no persistence across restarts, by design.
type State int

// The three states of FR-01. A switch over a State must handle all three — the
// exhaustive linter enforces it, because a missing case silently defaulting to
// Closed is the bug that turns a breaker into an expensive no-op.
const (
	// StateClosed passes calls through and counts failures against the
	// threshold of FR-02.
	StateClosed State = iota

	// StateOpen rejects calls without invoking the wrapped operation, until
	// the open timeout expires.
	StateOpen

	// StateHalfOpen admits a bounded number of probe calls to find out
	// whether the remote dependency has recovered.
	StateHalfOpen
)

// String returns the state's name, lowercase and stable. It is part of the API
// rather than a debugging convenience: a host will put it in a log line or a
// metric label, so it does not change once released.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// The transition table of FR-01, implemented in breaker.go's effectiveState,
// admit and complete rather than here — State itself stays a plain value type
// with no behaviour of its own:
//
//	Closed    -> Open        consecutive failures reach the threshold (FR-02)
//	Open      -> HalfOpen    the open timeout has elapsed, evaluated lazily on
//	                         the next call or read rather than by a timer, so
//	                         the Breaker owns no goroutine (NFR-05, IR-03)
//	HalfOpen  -> Closed      a probe call succeeds
//	HalfOpen  -> Open        a probe call fails, or its window's lease expires
//	                         without it returning (FR-12, ADR-0004)
//
// Every transition notifies Hooks.OnStateChange (FR-09) and every one is
// covered by a transition test in state_test.go, each seen failing against a
// deliberately broken implementation before being trusted (NFR-06).

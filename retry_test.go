package bastion_test

import (
	"testing"

	"github.com/JonasBorgesLM/bastion"
)

// The zero value must retry nothing. A RetryPolicy someone forgot to configure
// should behave like the absence of retry, not like an unbounded one: the
// failure mode of the opposite default is a policy that silently multiplies
// load against a dependency that is already failing.
func TestRetryPolicy_ZeroValueRetriesNothing(t *testing.T) {
	var p bastion.RetryPolicy
	if p.MaxAttempts > 1 {
		t.Fatalf("zero RetryPolicy.MaxAttempts = %d, want 0 or 1", p.MaxAttempts)
	}
	if p.Jitter != 0 {
		t.Fatalf("zero RetryPolicy.Jitter = %v, want 0", p.Jitter)
	}
}

// TODO(B3): the retry suite of FR-06, driven by fakeClock and a context, never
// by real elapsed time:
//
//   - a policy of one attempt calls the operation exactly once
//   - a successful first attempt does not retry
//   - attempts stop at MaxAttempts and the last error reaches the caller
//   - the delay doubles between attempts and is capped by MaxDelay
//   - jitter keeps every delay inside its expected band, asserted over many
//     draws rather than one — a bound that holds for a single sample is not a
//     bound
//   - a context cancelled mid-wait abandons the wait at once, does not sleep
//     out the remainder, and is not counted as a failure (FR-05)
//
// And the composition test that matters more than any of them: retry wrapped
// around a Breaker, asserting which of the two sees how many calls. That
// ordering is the subject of the "retry inside or composed around" ADR, and the
// test is what stops the ADR from being contradicted later by accident.

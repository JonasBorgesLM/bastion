package bastion_test

import (
	"testing"

	"github.com/JonasBorgesLM/bastion"
)

// The zero value of State is Closed, and that is load-bearing rather than
// incidental: a Breaker whose state field was never written, and a process that
// has just restarted, both pass calls through. If this ever changes, a restart
// starts every circuit in a state nothing has measured.
func TestState_ZeroValueIsClosed(t *testing.T) {
	var s bastion.State
	if s != bastion.StateClosed {
		t.Fatalf("zero State = %v, want %v", s, bastion.StateClosed)
	}
}

// The names are part of the API (FR-09): a host puts them in a log line or a
// metric label, so they are asserted literally rather than round-tripped.
func TestState_String(t *testing.T) {
	tests := []struct {
		name  string
		state bastion.State
		want  string
	}{
		{"closed", bastion.StateClosed, "closed"},
		{"open", bastion.StateOpen, "open"},
		{"half-open", bastion.StateHalfOpen, "half-open"},
		{"out of range", bastion.State(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Fatalf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

// TODO(B1): the transition suite of FR-01 and NFR-06, all of it driven by
// fakeClock rather than by sleeping:
//
//   - Closed stays Closed while failures remain below the threshold
//   - Closed opens on the failure that reaches the threshold, and not before
//   - a success in Closed resets the consecutive count
//   - Open rejects with ErrOpenState without invoking the operation
//   - Open stays Open until the timeout has fully elapsed — assert the
//     boundary from both sides, one nanosecond short and exactly on it
//   - Open admits a probe once the timeout elapsed, and State reports Half-Open
//   - Half-Open closes on a successful probe
//   - Half-Open reopens on a failed probe, and the timeout restarts
//   - Half-Open rejects with ErrTooManyRequests past its allowance
//
// Each of these must be seen failing before it is trusted (NFR-06).

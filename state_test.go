package bastion_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// Every transition test drives fakeClock rather than sleeping (NFR-05).
// "Open rejects without invoking the operation" is asserted in
// breaker_test.go's TestExecute_RejectedCallNeverInvokesTheOperation, since it
// is a property of Execute's call path rather than of State itself.

func newTestBreaker(t *testing.T, clock *fakeClock, opts ...bastion.Option) *bastion.Breaker {
	t.Helper()
	all := append([]bastion.Option{bastion.WithClock(clock)}, opts...)
	b, err := bastion.New("dep", all...)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	return b
}

func TestBreaker_ClosedStaysClosedBelowTheThreshold(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock, bastion.WithFailureThreshold(3))

	for range 2 {
		_, _ = bastion.Execute(context.Background(), b, failingOp)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after 2 of 3 failures = %v, want %v", got, bastion.StateClosed)
	}
}

// Closed opens on the failure that reaches the threshold, and not before --
// asserted by checking the state is still Closed after the second failure and
// only Open after the third, in the same test, so an off-by-one in the
// comparison shows up as a specific assertion failing rather than a vague one.
func TestBreaker_ClosedOpensOnTheFailureThatReachesTheThreshold(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock, bastion.WithFailureThreshold(3))

	_, _ = bastion.Execute(context.Background(), b, failingOp)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after 2 of 3 failures = %v, want %v (not yet)", got, bastion.StateClosed)
	}

	_, _ = bastion.Execute(context.Background(), b, failingOp)
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after the 3rd failure = %v, want %v", got, bastion.StateOpen)
	}
}

func TestBreaker_SuccessInClosedResetsTheConsecutiveCount(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock, bastion.WithFailureThreshold(3))

	_, _ = bastion.Execute(context.Background(), b, failingOp)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	_, _ = bastion.Execute(context.Background(), b, succeedingOp) // resets the count to 0
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	_, _ = bastion.Execute(context.Background(), b, failingOp)

	// Negative control: verified failing (State() == Open) against a Breaker
	// whose success path did not reset the counter -- 2+2 failures reaches a
	// threshold of 3 if the reset never happened.
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() = %v, want %v (only 2 consecutive failures since the reset)", got, bastion.StateClosed)
	}
}

// FR-01: Open must not admit a call before the timeout has fully elapsed.
// Negative control: verified failing against a breaker comparing elapsed > openTimeout
// rather than >=, which admits one nanosecond early.
func TestBreaker_OpenStaysOpenUntilTheTimeoutFullyElapses(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)

	clock.Advance(10*time.Second - time.Nanosecond)
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("one nanosecond short of the timeout: error = %v, want a match for ErrOpenState", err)
	}

	clock.Advance(time.Nanosecond) // exactly at the timeout now
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("exactly at the timeout: error = %v, want the probe admitted", err)
	}
}

// FR-13: State() reflects an elapsed timeout with no call in between --
// the lazy transition is evaluated on a read, not only on the next call.
func TestBreaker_StateReflectsAnElapsedTimeoutWithNoCallInBetween(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() right after opening = %v, want %v", got, bastion.StateOpen)
	}

	clock.Advance(10 * time.Second)
	if got := b.State(); got != bastion.StateHalfOpen {
		t.Fatalf("State() after the timeout elapsed, no call made = %v, want %v", got, bastion.StateHalfOpen)
	}
}

func TestBreaker_HalfOpenClosesOnASuccessfulProbe(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("probe error = %v, want nil", err)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after a successful probe = %v, want %v", got, bastion.StateClosed)
	}
}

// Half-Open reopens on a failed probe, and the timeout restarts from the
// failure -- not from the original Open.
func TestBreaker_HalfOpenReopensOnAFailedProbeAndTheTimeoutRestarts(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	_, _ = bastion.Execute(context.Background(), b, failingOp) // the probe itself fails
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after a failed probe = %v, want %v", got, bastion.StateOpen)
	}

	clock.Advance(10*time.Second - time.Nanosecond)
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("one nanosecond short of the restarted timeout: error = %v, want a match for ErrOpenState", err)
	}
	clock.Advance(time.Nanosecond)
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("exactly at the restarted timeout: error = %v, want the probe admitted", err)
	}
}

// Half-Open rejects past its allowance without invoking the operation.
// Requires two calls genuinely concurrent -- sequentially, the first probe
// always completes (admitting, then releasing its slot) before the second
// call is even attempted, so the allowance is never observed exhausted.
func TestBreaker_HalfOpenRejectsPastItsAllowance(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			close(started)
			<-release
			return 42, nil
		})
	}()
	<-started
	defer close(release)

	calls := 0
	op := func(context.Context) (int, error) { calls++; return 0, nil }
	if _, err := bastion.Execute(context.Background(), b, op); !errors.Is(err, bastion.ErrTooManyRequests) {
		t.Fatalf("second concurrent call during the probe: error = %v, want a match for ErrTooManyRequests", err)
	}
	if calls != 0 {
		t.Fatalf("operation invoked %d times past the half-open allowance, want 0", calls)
	}
}

// ADR-0004: a stale half-open probe expires back to Open, on the same
// openTimeout, rather than stranding the circuit rejecting everything forever.
// Negative control: verified failing (ErrTooManyRequests forever, State never
// leaving Half-Open) against a breaker whose Half-Open branch had no lease
// check at all.
func TestBreaker_StaleHalfOpenProbeExpiresBackToOpen(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	started := make(chan struct{})
	hang := make(chan struct{}) // never closed: simulates a probe that never returns
	go func() {
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			close(started)
			<-hang
			return 0, nil
		})
	}()
	<-started

	clock.Advance(10*time.Second - time.Nanosecond) // the probe's lease has not expired yet
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrTooManyRequests) {
		t.Fatalf("before the lease expires: error = %v, want a match for ErrTooManyRequests", err)
	}

	clock.Advance(time.Nanosecond) // exactly at the lease boundary now
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("once the lease expires: error = %v, want a match for ErrOpenState (reopened, not admitted)", err)
	}
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after the stale probe's lease expired = %v, want %v", got, bastion.StateOpen)
	}
}

// ADR-0004's generation token, isolated from the plainer "state has already
// moved on" guard it sits next to. If probe P1's window (generation G1)
// merely expires to Open and stays there, a bare `state == HalfOpen` check
// would already discard P1's late completion correctly -- that is not what
// this test is for. This test drives the breaker all the way back to a
// *second* Half-Open window (generation G2) before P1 finally returns, so
// `state == HalfOpen` is true again by then and only the generation
// mismatch (G1 != G2) can tell P1's stale result apart from P2's, the probe
// that actually belongs to the current window.
//
// Negative control: verified failing (State() == Closed after P1's stale
// success) with the generation check removed but the state-equality guard
// left in place -- see the session record for the mutation that produced
// this failure. A generation-less breaker cannot tell P1's success apart
// from P2's, because both observe state == HalfOpen as true.
func TestBreaker_StaleHalfOpenProbesLateCompletionIsDiscardedAcrossAWindow(t *testing.T) {
	clock := newFakeClock()
	b := newTestBreaker(t, clock,
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
	)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second) // Open's timeout elapses

	// P1: generation G1's probe. Hangs until released, deliberately late.
	p1Started := make(chan struct{})
	p1Release := make(chan struct{})
	p1Done := make(chan error, 1)
	go func() {
		_, err := bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			close(p1Started)
			<-p1Release
			return 42, nil // would CLOSE the circuit, if this completion were honored
		})
		p1Done <- err
	}()
	<-p1Started

	clock.Advance(10 * time.Second) // G1's lease expires: HalfOpen -> Open
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("error = %v, want a match for ErrOpenState (G1 reopened)", err)
	}

	clock.Advance(10 * time.Second) // Open's timeout elapses a second time
	// P2: generation G2's probe, admitted by this very call. Left in flight
	// deliberately, so G2 is still open (not yet Closed by its own probe's
	// success) when P1's stale completion arrives below.
	p2Started := make(chan struct{})
	p2Release := make(chan struct{})
	p2Done := make(chan struct{})
	go func() {
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			close(p2Started)
			<-p2Release
			return 0, errBoom
		})
		close(p2Done)
	}()
	<-p2Started
	if got := b.State(); got != bastion.StateHalfOpen {
		t.Fatalf("State() with G2's probe in flight = %v, want %v", got, bastion.StateHalfOpen)
	}

	close(p1Release) // P1 (G1) finally returns, successfully
	if err := <-p1Done; err != nil {
		t.Fatalf("P1's own Execute error = %v, want nil (it succeeded)", err)
	}

	if got := b.State(); got != bastion.StateHalfOpen {
		t.Fatalf("State() after P1's stale, late success = %v, want %v (G2 is unaffected; P1's result must be discarded)", got, bastion.StateHalfOpen)
	}

	close(p2Release) // let G2's own probe finish and reopen the circuit
	<-p2Done
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after G2's own probe failed = %v, want %v", got, bastion.StateOpen)
	}
}

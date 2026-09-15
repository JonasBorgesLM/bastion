package bastion_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// ADR-0017: Trip forces StateOpen immediately, from any prior state, and
// reports the transition as Manual.
func TestTrip_ForcesOpenFromClosed(t *testing.T) {
	var events []bastion.StateChangeEvent
	b, err := bastion.New("dep", bastion.WithHooks(bastion.Hooks{
		OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
			events = append(events, ev)
		},
	}))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	b.Trip(context.Background())

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after Trip = %v, want %v", got, bastion.StateOpen)
	}
	if len(events) != 1 {
		t.Fatalf("got %d OnStateChange events, want 1: %+v", len(events), events)
	}
	if ev := events[0]; ev.From != bastion.StateClosed || ev.To != bastion.StateOpen || !ev.Manual {
		t.Fatalf("event = %+v, want {From: Closed, To: Open, Manual: true}", ev)
	}
}

// A tripped circuit rejects every call -- op must never run.
func TestTrip_RejectsEveryCallUntilReset(t *testing.T) {
	b, err := bastion.New("dep")
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	b.Trip(context.Background())

	calls := 0
	for range 3 {
		_, err := bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			calls++
			return 42, nil
		})
		if !errors.Is(err, bastion.ErrOpenState) {
			t.Fatalf("Execute() error = %v, want a match for ErrOpenState", err)
		}
	}
	if calls != 0 {
		t.Fatalf("op invoked %d times against a tripped circuit, want 0", calls)
	}
}

// ADR-0017's central claim: unlike an evidence-driven Open, a manual trip
// does not expire on WithOpenTimeout.
//
// Negative control: verified failing (State() == HalfOpen after the
// advance) against a version of effectiveState that did not check
// manualTrip before falling into the ordinary openedAt/openTimeout
// comparison.
func TestTrip_DoesNotExpireOnOpenTimeout(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithOpenTimeout(10*time.Second), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	b.Trip(context.Background())

	clock.Advance(24 * time.Hour) // far past any ordinary openTimeout

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() long after Trip, timeout elapsed many times over = %v, want %v (a manual trip must not expire)", got, bastion.StateOpen)
	}
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("Execute() after the timeout elapsed = %v, want a match for ErrOpenState (no probe should be admitted)", err)
	}
}

// Calling Trip while already Open -- evidence-driven or already manually
// tripped -- re-affirms it without firing a second event: From and To would
// both be StateOpen, the same "stays put, no event" rule every other
// transition follows.
func TestTrip_CalledAgainWhileAlreadyOpenDoesNotFireASecondEvent(t *testing.T) {
	events := 0
	b, err := bastion.New("dep", bastion.WithHooks(bastion.Hooks{
		OnStateChange: func(context.Context, bastion.StateChangeEvent) { events++ },
	}))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	b.Trip(context.Background())
	b.Trip(context.Background())

	if events != 1 {
		t.Fatalf("got %d OnStateChange events across two Trip calls, want 1", events)
	}
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() = %v, want %v", got, bastion.StateOpen)
	}
}

// ADR-0004's stale-generation discard must also protect a manual trip: a
// Half-Open probe admitted before Trip is called must not be allowed to
// resolve the window Trip has since forced open.
//
// State() and Counts().Manual alone cannot tell a passing run from a
// broken one here: manualTrip's own effectiveState short-circuit reports
// StateOpen regardless of what b.state actually holds underneath, so a
// version of Trip that forgets to bump halfOpenGeneration would still pass
// those two checks -- the corruption it leaves in the raw b.state is
// invisible until something else reads b.state directly, which the very
// next admit() call does. That call's own "eff != b.state" transition
// check would then "notice" a bogus transition and fire a spurious,
// misleading OnStateChange event claiming the circuit left Open, when from
// the operator's perspective it never did. That fourth event is the actual
// negative-control signal used below.
//
// Negative control: verified failing (2 extra events fire beyond the 3
// expected -- {From: StateOpen, To: StateClosed} misreporting the stale
// probe's success as a real transition, then {From: StateClosed, To:
// StateOpen} as the next call's admit() re-notices the manual trip) against
// a version of Trip that did not bump halfOpenGeneration / reset
// halfOpenInFlight.
func TestTrip_DiscardsAnInFlightHalfOpenProbe(t *testing.T) {
	clock := newFakeClock()
	var events []bastion.StateChangeEvent
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				events = append(events, ev)
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp) // Closed -> Open
	clock.Advance(10 * time.Second)                            // Open's timeout elapses; not yet noticed

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	probeDone := make(chan error, 1)
	go func() {
		_, err := bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			close(probeStarted)
			<-releaseProbe
			return 42, nil // the probe itself succeeds
		})
		probeDone <- err
	}()
	<-probeStarted // admit() has already fired Open -> HalfOpen to admit this probe

	b.Trip(context.Background()) // operator forces Open while the probe is still in flight

	close(releaseProbe)
	if err := <-probeDone; err != nil {
		t.Fatalf("the in-flight probe's own call: error = %v, want nil (only its effect on the breaker must be discarded)", err)
	}

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after the stale probe's success completed = %v, want %v (Trip must not be overridden by a probe admitted before it)", got, bastion.StateOpen)
	}
	if got := b.Counts().Manual; !got {
		t.Fatal("Counts().Manual = false after the stale probe completed, want true (the manual trip must survive)")
	}

	// One more call -- rejected, since the circuit is manually Open -- is
	// what would surface admit()'s own self-correction of a corrupted
	// b.state as a spurious event, per the comment above.
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("Execute() error = %v, want a match for ErrOpenState", err)
	}

	want := []bastion.StateChangeEvent{
		{Name: "dep", From: bastion.StateClosed, To: bastion.StateOpen},                 // evidence
		{Name: "dep", From: bastion.StateOpen, To: bastion.StateHalfOpen},               // evidence, admitting the probe
		{Name: "dep", From: bastion.StateHalfOpen, To: bastion.StateOpen, Manual: true}, // Trip
	}
	if len(events) != len(want) {
		t.Fatalf("got %d OnStateChange events, want %d: %+v", len(events), len(want), events)
	}
	for i, ev := range events {
		if ev != want[i] {
			t.Fatalf("event[%d] = %+v, want %+v", i, ev, want[i])
		}
	}
}

// ADR-0017: Reset clears the state, ConsecutiveFailures, and any manual
// trip, unconditionally to StateClosed, and reports the transition as
// Manual.
func TestReset_ClearsStateConsecutiveFailuresAndManualTrip(t *testing.T) {
	var events []bastion.StateChangeEvent
	b, err := bastion.New("dep", bastion.WithHooks(bastion.Hooks{
		OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
			events = append(events, ev)
		},
	}))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	b.Trip(context.Background())
	events = nil // discard Trip's own event; this test is about Reset's

	b.Reset(context.Background())

	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after Reset = %v, want %v", got, bastion.StateClosed)
	}
	c := b.Counts()
	if c.Manual {
		t.Fatal("Counts().Manual after Reset = true, want false")
	}
	if len(events) != 1 {
		t.Fatalf("got %d OnStateChange events, want 1: %+v", len(events), events)
	}
	if ev := events[0]; ev.From != bastion.StateOpen || ev.To != bastion.StateClosed || !ev.Manual {
		t.Fatalf("event = %+v, want {From: Open, To: Closed, Manual: true}", ev)
	}

	// A fresh call must actually run op, not be rejected.
	ran := false
	if _, err := bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
		ran = true
		return 42, nil
	}); err != nil {
		t.Fatalf("Execute() after Reset: error = %v, want nil", err)
	}
	if !ran {
		t.Fatal("op did not run after Reset -- the circuit is still rejecting")
	}
}

// Reset must clear ConsecutiveFailures even when the circuit never left
// StateClosed -- a partial failure count close to FailureThreshold is
// exactly the stale evidence an operator confirming a fix wants cleared.
//
// Negative control: verified failing (ConsecutiveFailures == 2) against a
// version of Reset that set state and manualTrip but never touched
// consecutiveFailures.
func TestReset_ClearsConsecutiveFailuresEvenWhileStillClosed(t *testing.T) {
	b, err := bastion.New("dep", bastion.WithFailureThreshold(5))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	if got := b.Counts().ConsecutiveFailures; got != 2 {
		t.Fatalf("setup: Counts().ConsecutiveFailures = %d, want 2", got)
	}

	b.Reset(context.Background())

	if got := b.Counts().ConsecutiveFailures; got != 0 {
		t.Fatalf("Counts().ConsecutiveFailures after Reset = %d, want 0", got)
	}
}

// Calling Reset while already Closed must not fire an event -- the same
// "stays put, no event" rule Trip's own idempotency test already covers.
func TestReset_OnAnAlreadyClosedCircuitDoesNotFireAnEvent(t *testing.T) {
	events := 0
	b, err := bastion.New("dep", bastion.WithHooks(bastion.Hooks{
		OnStateChange: func(context.Context, bastion.StateChangeEvent) { events++ },
	}))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	b.Reset(context.Background())

	if events != 0 {
		t.Fatalf("got %d OnStateChange events from Reset on an already-Closed circuit, want 0", events)
	}
}

// Counts.Manual must be false for an ordinary, evidence-driven Open --
// Manual reports a standing Trip specifically, not "is the circuit Open for
// any reason." Not independently negative-control-verified beyond this: by
// construction, manualTrip can only be true when effectiveState already
// forces StateOpen (TestTrip_DoesNotExpireOnOpenTimeout's own negative
// control covers that coupling), so the two are not independently
// mutatable here.
func TestCounts_ManualIsFalseForAnEvidenceDrivenOpen(t *testing.T) {
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)

	c := b.Counts()
	if c.State != bastion.StateOpen {
		t.Fatalf("Counts().State = %v, want %v", c.State, bastion.StateOpen)
	}
	if c.Manual {
		t.Fatal("Counts().Manual = true for an evidence-driven Open, want false")
	}
}

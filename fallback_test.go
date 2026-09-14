package bastion_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JonasBorgesLM/bastion"
)

func TestFallback_ReturnsTheOriginalResultWhenErrIsNil(t *testing.T) {
	fbCalls := 0
	got, err := bastion.Fallback(context.Background(), 42, nil, func(context.Context, error) (int, error) {
		fbCalls++
		return 0, errBoom
	})
	if err != nil || got != 42 {
		t.Fatalf("Fallback() = (%d, %v), want (42, nil)", got, err)
	}
	if fbCalls != 0 {
		t.Fatalf("fb invoked %d times on a nil error, want 0", fbCalls)
	}
}

// ADR-0008: on a non-nil err, Fallback calls fb and returns exactly what fb
// returns -- whether the err came from an outright rejection (ErrOpenState,
// ErrTooManyRequests) or a genuine operation failure, since Fallback cannot
// and does not need to distinguish the two (FR-08's own wording asks for
// both).
func TestFallback_CallsFbOnRejection(t *testing.T) {
	got, err := bastion.Fallback(context.Background(), 0, bastion.ErrOpenState, func(context.Context, error) (int, error) {
		return 99, nil
	})
	if err != nil || got != 99 {
		t.Fatalf("Fallback() = (%d, %v), want (99, nil)", got, err)
	}
}

func TestFallback_CallsFbOnAGenuineOperationFailure(t *testing.T) {
	got, err := bastion.Fallback(context.Background(), 0, errBoom, func(context.Context, error) (int, error) {
		return 99, nil
	})
	if err != nil || got != 99 {
		t.Fatalf("Fallback() = (%d, %v), want (99, nil)", got, err)
	}
}

// A fallback that itself fails: its own error reaches the caller, not the
// original one -- fb's outcome replaces the original entirely.
func TestFallback_AFallbackThatItselfFailsReturnsItsOwnError(t *testing.T) {
	fbErr := errors.New("fallback also failed")
	_, err := bastion.Fallback(context.Background(), 0, errBoom, func(context.Context, error) (int, error) {
		return 0, fbErr
	})
	if !errors.Is(err, fbErr) {
		t.Fatalf("Fallback() error = %v, want a match for the fallback's own error", err)
	}
}

// ADR-0008's cancellation exception: a caller who already cancelled ctx does
// not want fb run on their behalf. The ORIGINAL err reaches the caller
// unchanged, not fb's.
func TestFallback_DoesNotRunWhenContextIsAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fbCalls := 0
	_, err := bastion.Fallback(ctx, 0, errBoom, func(context.Context, error) (int, error) {
		fbCalls++
		return 99, nil
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Fallback() error = %v, want the original error (errBoom) unchanged", err)
	}
	if fbCalls != 0 {
		t.Fatalf("fb invoked %d times against an already-cancelled context, want 0", fbCalls)
	}
}

// The property ADR-0008 states must hold by construction: Fallback never
// touches a Breaker, so its own outcome -- success or failure -- cannot
// corrupt the circuit's counters. Verified against the breaker's OBSERVABLE
// state, not by inspecting Fallback's implementation.
func TestFallback_DoesNotCorruptBreakerCountersOnEitherOutcome(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	result, execErr := bastion.Execute(context.Background(), b, failingOp)
	if execErr == nil {
		t.Fatal("setup: expected the call to fail and open the circuit")
	}
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after the failing call = %v, want %v", got, bastion.StateOpen)
	}

	// A succeeding fallback must not somehow "heal" the breaker.
	_, _ = bastion.Fallback(context.Background(), result, execErr, func(context.Context, error) (int, error) {
		return 42, nil
	})
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after a succeeding fallback = %v, want %v (fallback must not touch the breaker)", got, bastion.StateOpen)
	}

	// A failing fallback must not somehow trip it further or differently.
	_, _ = bastion.Fallback(context.Background(), result, execErr, func(context.Context, error) (int, error) {
		return 0, errors.New("fallback also failed")
	})
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after a failing fallback = %v, want %v (fallback must not touch the breaker)", got, bastion.StateOpen)
	}
}

// The intended, documented composition (ADR-0008): called strictly after
// Execute has already returned, at the call site.
func TestFallback_ComposesAfterExecute(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	result, err := bastion.Execute(context.Background(), b, failingOp)
	result, err = bastion.Fallback(context.Background(), result, err, func(context.Context, error) (int, error) {
		return 7, nil
	})
	if err != nil || result != 7 {
		t.Fatalf("Fallback(Execute(...)) = (%d, %v), want (7, nil)", result, err)
	}
}

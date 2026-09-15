package bastion_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JonasBorgesLM/bastion"
)

// Do's own return value is exactly op's error, on both paths.
func TestDo_RunsOpAndReturnsItsError(t *testing.T) {
	b, err := bastion.New("dep")
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	if err := bastion.Do(context.Background(), b, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	if err := bastion.Do(context.Background(), b, func(context.Context) error {
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatalf("Do() error = %v, want a match for errBoom", err)
	}
}

// issue #57's own requirement: Do must share Execute's accounting exactly,
// not a second code path that could drift from it. Proven the same way any
// other Execute caller's admission is proven -- a rejected call does not
// invoke op at all.
//
// Negative control: verified failing (calls == 1, the real dependency
// invoked despite the open circuit) against a version of Do that called op
// directly -- return op(ctx) -- instead of routing through Execute.
func TestDo_RejectsWithoutRunningOpWhenOpen(t *testing.T) {
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if err := bastion.Do(context.Background(), b, func(context.Context) error {
		return errBoom
	}); err == nil {
		t.Fatal("setup: expected the call to fail and open the circuit")
	}

	calls := 0
	if err := bastion.Do(context.Background(), b, func(context.Context) error {
		calls++
		return nil
	}); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("Do() error = %v, want a match for ErrOpenState", err)
	}
	if calls != 0 {
		t.Fatalf("op invoked %d times against an open circuit, want 0", calls)
	}
}

// A failure through Do must count toward FailureThreshold exactly the way
// one through Execute does -- the same counter, not a parallel one.
func TestDo_FailuresCountTowardTheSameThresholdAsExecute(t *testing.T) {
	b, err := bastion.New("dep", bastion.WithFailureThreshold(2))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	_ = bastion.Do(context.Background(), b, func(context.Context) error { return errBoom })
	if got := b.Counts().ConsecutiveFailures; got != 1 {
		t.Fatalf("Counts().ConsecutiveFailures after 1 failure via Do = %d, want 1", got)
	}

	if _, err := bastion.Execute(context.Background(), b, failingOp); err == nil {
		t.Fatal("setup: expected the 2nd failure (via Execute) to trip the circuit")
	}
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after 1 Do failure + 1 Execute failure = %v, want %v (both must count toward the same threshold)", got, bastion.StateOpen)
	}
}

// A success through Do must reset ConsecutiveFailures, the same as Execute.
func TestDo_SuccessResetsConsecutiveFailures(t *testing.T) {
	b, err := bastion.New("dep", bastion.WithFailureThreshold(5))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_ = bastion.Do(context.Background(), b, func(context.Context) error { return errBoom })
	_ = bastion.Do(context.Background(), b, func(context.Context) error { return errBoom })

	_ = bastion.Do(context.Background(), b, func(context.Context) error { return nil })

	if got := b.Counts().ConsecutiveFailures; got != 0 {
		t.Fatalf("Counts().ConsecutiveFailures after a success via Do = %d, want 0", got)
	}
}

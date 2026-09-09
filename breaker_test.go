package bastion_test

import (
	"errors"
	"testing"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// A breaker's name appears in every event the hooks emit (FR-09), so an unnamed
// one produces events an operator cannot attribute. New refuses rather than
// inventing a name.
func TestNew_RejectsAnEmptyName(t *testing.T) {
	b, err := bastion.New("")
	if !errors.Is(err, bastion.ErrInvalidConfig) {
		t.Fatalf("New(\"\") error = %v, want one matching ErrInvalidConfig", err)
	}
	if b != nil {
		t.Fatalf("New(\"\") returned a breaker alongside an error: %#v", b)
	}
}

func TestNew_StartsClosed(t *testing.T) {
	b, err := bastion.New("task-api")
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() = %v, want %v", got, bastion.StateClosed)
	}
	if got := b.Name(); got != "task-api" {
		t.Fatalf("Name() = %q, want %q", got, "task-api")
	}
}

// Options are accepted and the breaker is still usable afterwards. This is
// deliberately shallow: what each option *does* is asserted by the behaviour
// tests that arrive with the behaviour, not by reaching into private fields.
func TestNew_AcceptsOptions(t *testing.T) {
	clock := newFakeClock()

	b, err := bastion.New("task-api",
		bastion.WithFailureThreshold(3),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(2),
		bastion.WithIsFailure(func(error) bool { return true }),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() = %v, want %v", got, bastion.StateClosed)
	}
}

// Two breakers sharing a name are still two independent breakers: New registers
// nothing in a package-level table, so there is nothing for them to collide in
// (FR-10, IR-03).
func TestNew_BreakersAreIndependent(t *testing.T) {
	first, err := bastion.New("task-api")
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	second, err := bastion.New("task-api")
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if first == second {
		t.Fatal("New returned the same breaker twice for the same name; there is a registry somewhere")
	}
}

// TODO(B4): New must reject a non-positive threshold, open timeout and
// half-open allowance. Each rejection gets a case here, and each is written
// against a New that does not yet check — seen red, then made green.
//
// TODO(B1): the behaviour suite for the entry point, once its name and
// signature are settled by ADR. The state-transition cases are listed in
// state_test.go; what belongs here is the call path itself — that a rejected
// call never invokes the operation, that the operation's return value and error
// reach the caller unchanged, and that a cancelled context is accounted for as
// neither success nor failure (FR-05).
//
// TODO(B6): the concurrency suite of NFR-01 and the overhead benchmarks of
// NFR-02, the latter in breaker_bench_test.go.

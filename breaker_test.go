package bastion_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// Shared across breaker_test.go and state_test.go: a fixed failing and a
// fixed succeeding operation, so every transition test states its intent
// (failingOp, succeedingOp) rather than an inline closure repeated fifteen
// times with fifteen chances to typo one.
var errBoom = errors.New("test: boom")

func failingOp(context.Context) (int, error)    { return 0, errBoom }
func succeedingOp(context.Context) (int, error) { return 42, nil }

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

// IR-04: New returns ErrInvalidConfig rather than building a Breaker that
// would misbehave -- or, for a nil Clock, panic -- at the first call.
func TestNew_RejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opts []bastion.Option
	}{
		{"zero FailureThreshold", []bastion.Option{bastion.WithFailureThreshold(0)}},
		{"negative FailureThreshold", []bastion.Option{bastion.WithFailureThreshold(-1)}},
		{"zero OpenTimeout", []bastion.Option{bastion.WithOpenTimeout(0)}},
		{"negative OpenTimeout", []bastion.Option{bastion.WithOpenTimeout(-time.Second)}},
		{"zero HalfOpenMaxCalls", []bastion.Option{bastion.WithHalfOpenMaxCalls(0)}},
		{"negative HalfOpenMaxCalls", []bastion.Option{bastion.WithHalfOpenMaxCalls(-1)}},
		{"nil Clock", []bastion.Option{bastion.WithClock(nil)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := bastion.New("dep", tt.opts...)
			if !errors.Is(err, bastion.ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want a match for ErrInvalidConfig", err)
			}
			if b != nil {
				t.Fatalf("New() returned a breaker alongside an error: %#v", b)
			}
		})
	}
}

// The boundary itself: exactly 1 is valid for all three integer options,
// distinguishing "must be positive" from "must be at least 2 or more".
func TestNew_AcceptsTheMinimumPositiveValues(t *testing.T) {
	clock := newFakeClock()
	_, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(time.Nanosecond),
		bastion.WithHalfOpenMaxCalls(1),
		bastion.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
}

// ADR-0005: a cancelled context moves neither counter. FailureThreshold(1)
// means a single counted failure would open the circuit -- it stays Closed,
// proving the cancelled call was excluded rather than merely outnumbered.
func TestExecute_CancelledContextDoesNotCountAsAFailure(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = bastion.Execute(ctx, b, failingOp)

	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after a cancelled call returning an error = %v, want %v", got, bastion.StateClosed)
	}
}

// ADR-0005: a cancelled context does not reset the consecutive-failure count
// either -- it is excluded from accounting entirely, not treated as a
// success. FailureThreshold(2): one real failure, then a cancelled call
// (returning success, to isolate the reset question specifically), then a
// second real failure. If the cancelled call had wrongly reset the count,
// this second failure would only bring it to 1 and the circuit would stay
// Closed; correctly excluded, the count is still 1 going in and this failure
// reaches 2, opening it.
//
// Negative control: verified failing (State() == Closed) against a version
// that folded the cancelled outcome into the same branch as success.
func TestExecute_CancelledContextDoesNotResetTheFailureCount(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(2), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	_, _ = bastion.Execute(context.Background(), b, failingOp) // 1 real failure

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = bastion.Execute(ctx, b, succeedingOp) // cancelled; must not reset

	_, _ = bastion.Execute(context.Background(), b, failingOp) // 2nd real failure

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after 2 real failures around a cancelled call = %v, want %v", got, bastion.StateOpen)
	}
}

// ADR-0005's actual distinguishing claim: an operation's OWN internally
// derived context timing out is not the same event as the caller's context
// (the one passed into Execute) being cancelled, even though both can
// produce context.DeadlineExceeded. Here the outer ctx is context.Background
// -- never cancelled -- so this must count as an ordinary failure.
//
// Negative control: verified failing (State() == Closed) against a version
// matching on errors.Is(err, context.DeadlineExceeded) / context.Canceled
// instead of reading ctx.Err() on the outer context -- exactly the
// alternative ADR-0005 rejects.
func TestExecute_OperationsOwnDeadlineExceededCountsAsAnOrdinaryFailure(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
		// Simulates an operation whose own internally-derived context (its
		// own shorter WithTimeout) expired -- unrelated to the outer ctx,
		// which the caller never touched.
		return 0, context.DeadlineExceeded
	})

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after the operation's own DeadlineExceeded (outer ctx untouched) = %v, want %v", got, bastion.StateOpen)
	}
}

// FR-09: OnCall reports Counted=false for a cancelled call, matching hooks.go's
// own doc comment on CallEvent.
func TestExecute_OnCallReportsCountedFalseForACancelledContext(t *testing.T) {
	clock := newFakeClock()
	var calls []bastion.CallEvent
	b, err := bastion.New("dep",
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnCall: func(_ context.Context, ev bastion.CallEvent) { calls = append(calls, ev) },
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = bastion.Execute(ctx, b, failingOp)

	if len(calls) != 1 {
		t.Fatalf("got %d OnCall events, want 1: %+v", len(calls), calls)
	}
	if calls[0].Counted {
		t.Fatalf("call event = %+v, want Counted=false for a cancelled context", calls[0])
	}
}

// ADR-0005's Consequences: a cancelled Half-Open probe frees its slot without
// resolving the window -- the generation is not bumped, so a later call is
// still admitted as a probe under the same window rather than being rejected
// with ErrTooManyRequests, and can still close the circuit normally.
func TestExecute_CancelledHalfOpenProbeFreesItsSlotWithoutResolvingTheWindow(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
		bastion.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = bastion.Execute(ctx, b, failingOp) // admitted as the probe, then cancelled
	if got := b.State(); got != bastion.StateHalfOpen {
		t.Fatalf("State() after the cancelled probe = %v, want %v (window still undecided)", got, bastion.StateHalfOpen)
	}

	// A fresh, non-cancelled call must be admitted as a probe (slot freed,
	// same window), not rejected -- and it resolves the window normally.
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("probe after the cancelled one: error = %v, want nil (admitted, not ErrTooManyRequests)", err)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after the window's real probe succeeded = %v, want %v", got, bastion.StateClosed)
	}
}

// ADR-0001: Execute is a free generic function. A rejected call must never
// invoke the operation -- ErrOpenState and ErrTooManyRequests both mean the
// call was refused before op ran, not that it ran and failed.
func TestExecute_RejectedCallNeverInvokesTheOperation(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if _, err := bastion.Execute(context.Background(), b, failingOp); err == nil {
		t.Fatal("setup: expected the first call to fail and open the circuit")
	}

	calls := 0
	op := func(context.Context) (int, error) { calls++; return 0, nil }
	if _, err := bastion.Execute(context.Background(), b, op); !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("Execute error = %v, want a match for ErrOpenState", err)
	}
	if calls != 0 {
		t.Fatalf("operation invoked %d times against an open circuit, want 0", calls)
	}
}

// The operation's own return value and error reach the caller unchanged --
// Execute does not wrap, retype, or annotate either.
func TestExecute_ReturnsTheOperationsValueAndErrorUnchanged(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	got, err := bastion.Execute(context.Background(), b, func(context.Context) (string, error) {
		return "hello", nil
	})
	if err != nil || got != "hello" {
		t.Fatalf("Execute() = (%q, %v), want (%q, nil)", got, err, "hello")
	}

	wantErr := errors.New("a specific sentinel the caller checks with errors.Is")
	_, err = bastion.Execute(context.Background(), b, func(context.Context) (string, error) {
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want a match for %v", err, wantErr)
	}
}

// FR-11: a panic in the operation reaches the caller unchanged -- the exact
// recovered value, not a wrapped or re-typed one.
func TestExecute_PanicIsReRaisedUnchanged(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	defer func() {
		r := recover()
		if r != "boom" {
			t.Fatalf("recovered = %v, want the original panic value %q", r, "boom")
		}
	}()
	_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
		panic("boom")
	})
	t.Fatal("Execute returned normally; want it to re-panic")
}

// ADR-0003: a panic always counts as a failure.
func TestExecute_PanicCountsAsAFailureAndOpensTheCircuit(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	func() {
		defer func() { _ = recover() }()
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			panic("boom")
		})
	}()

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after a panicking operation = %v, want %v", got, bastion.StateOpen)
	}
}

// FR-11: a panic must not leave b.mu held.
// Negative control: verified failing (State deadlocking on the second
// goroutine) against a version of Execute that held the lock across the call
// to op instead of releasing it before invoking the operation.
func TestExecute_PanicReleasesTheLockAndLeavesTheBreakerUsable(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	func() {
		defer func() { _ = recover() }()
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			panic("boom")
		})
	}()

	done := make(chan struct{})
	go func() {
		_ = b.State() // would block forever if b.mu were still held
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("State() did not return -- b.mu is still held after the panic")
	}
}

// ADR-0010, issue #46: reproduces the reported defect directly. An
// OnStateChange handler that panics on the Closed->HalfOpen transition used
// to leave halfOpenInFlight incremented forever, since complete (which would
// have released it) was never reached -- every later call, from every other
// caller, got ErrTooManyRequests until ADR-0004's staleness lease expired.
//
// Negative control: verified failing (State() == Open, the pre-fix leaked
// shape, and the follow-up Execute() returning ErrTooManyRequests) against
// the pre-ADR-0010 Execute, which called b.fireStateChange(ctx,
// adm.transition) unguarded and let its panic abort Execute before complete
// ever ran.
func TestExecute_AdmissionHookPanicDoesNotLeakTheProbeSlot(t *testing.T) {
	clock := newFakeClock()
	panicOnTransition := false
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				if panicOnTransition && ev.To == bastion.StateHalfOpen {
					panic("hook exploded")
				}
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	panicOnTransition = true
	opRan := false
	func() {
		defer func() { _ = recover() }()
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			opRan = true
			return 42, nil
		})
	}()
	panicOnTransition = false

	if opRan {
		t.Fatal("op ran despite the admission hook panicking before it -- ADR-0010 says it must not")
	}
	if got := b.State(); got != bastion.StateHalfOpen {
		t.Fatalf("State() after the admission hook panicked = %v, want %v (window left undecided, slot freed)", got, bastion.StateHalfOpen)
	}
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("Execute() after the panicking hook = %v, want nil (a fresh probe must still be admitted, not ErrTooManyRequests)", err)
	}
}

// ADR-0010: the admission hook's panic is re-raised to Execute's own caller,
// after bookkeeping is resolved -- a hook bug is exactly as visible as an op
// bug (ADR-0003), never silently swallowed.
func TestExecute_AdmissionHookPanicIsReRaisedToTheCaller(t *testing.T) {
	clock := newFakeClock()
	panicOnTransition := false
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				if panicOnTransition && ev.To == bastion.StateHalfOpen {
					panic("hook exploded")
				}
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)
	panicOnTransition = true

	defer func() {
		r := recover()
		if r != "hook exploded" {
			t.Fatalf("recovered = %v, want the hook's own panic value %q", r, "hook exploded")
		}
	}()
	_, _ = bastion.Execute(context.Background(), b, succeedingOp)
	t.Fatal("Execute returned normally; want it to re-panic with the hook's value")
}

// ADR-0010: if op itself also panics, op's panic is what reaches the
// caller -- ADR-0003's unconditional priority is unchanged by this fix.
func TestExecute_OpPanicTakesPriorityOverACompletionHookPanic(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep",
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnCall: func(context.Context, bastion.CallEvent) {
				panic("hook also exploded")
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	defer func() {
		r := recover()
		if r != "op exploded" {
			t.Fatalf("recovered = %v, want the operation's own panic value %q (ADR-0003's priority over a hook panic)", r, "op exploded")
		}
	}()
	_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
		panic("op exploded")
	})
	t.Fatal("Execute returned normally; want it to re-panic")
}

// ADR-0010: a panic in a completion-stage hook (OnCall, here) is re-raised
// only after bookkeeping has already happened -- FailureThreshold(1) must
// have opened the circuit before the panic reaches this test.
func TestExecute_CompletionHookPanicIsReRaisedAfterBookkeeping(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnCall: func(context.Context, bastion.CallEvent) {
				panic("OnCall exploded")
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	defer func() {
		r := recover()
		if r != "OnCall exploded" {
			t.Fatalf("recovered = %v, want the hook's own panic value", r)
		}
		if got := b.State(); got != bastion.StateOpen {
			t.Fatalf("State() after the panicking OnCall hook = %v, want %v (bookkeeping must complete before the panic reaches the caller)", got, bastion.StateOpen)
		}
	}()
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	t.Fatal("Execute returned normally; want it to re-panic with the hook's value")
}

// A panicking OnReject is re-raised too, for the rejection path -- there is
// no bookkeeping to protect there (a rejection resolves entirely inside
// admit), but the hook's own bug must still surface, consistently with every
// other hook call site.
func TestExecute_OnRejectPanicIsReRaised(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnReject: func(context.Context, bastion.RejectEvent) {
				panic("OnReject exploded")
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp) // opens the circuit

	defer func() {
		r := recover()
		if r != "OnReject exploded" {
			t.Fatalf("recovered = %v, want the hook's own panic value", r)
		}
	}()
	_, _ = bastion.Execute(context.Background(), b, succeedingOp) // rejected; OnReject panics
	t.Fatal("Execute returned normally; want it to re-panic with the hook's value")
}

// ADR-0010: OnCall's Err names specifically that a hook prevented the
// operation from running, distinguishing this case from an excused error or
// a cancelled context, both of which also report Counted=false.
func TestExecute_OnCallCarriesASpecificErrorWhenAHookPreventsOpFromRunning(t *testing.T) {
	clock := newFakeClock()
	panicOnTransition := false
	var calls []bastion.CallEvent
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				if panicOnTransition && ev.To == bastion.StateHalfOpen {
					panic("boom")
				}
			},
			OnCall: func(_ context.Context, ev bastion.CallEvent) {
				calls = append(calls, ev)
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp) // fires its own OnCall; not what this test is about
	clock.Advance(10 * time.Second)
	calls = nil

	panicOnTransition = true
	func() {
		defer func() { _ = recover() }()
		_, _ = bastion.Execute(context.Background(), b, succeedingOp)
	}()
	panicOnTransition = false

	if len(calls) != 1 {
		t.Fatalf("got %d OnCall events, want 1: %+v", len(calls), calls)
	}
	if calls[0].Err == nil || calls[0].Counted {
		t.Fatalf("call event = %+v, want a non-nil Err and Counted=false", calls[0])
	}
}

// ADR-0010: op calling runtime.Goexit (e.g. t.Fatal misused inside an
// operation closure) is accounted the same as an ordinary panic in op --
// unconditionally a failure -- and does not leak the probe slot either,
// verified from a second Execute call after the goroutine that ran the first
// one has terminated.
func TestExecute_OpGoexitIsAccountedAsFailureAndDoesNotLeakTheSlot(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
		bastion.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			runtime.Goexit()
			return 42, nil // unreachable
		})
	}()
	<-done

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after op called Goexit = %v, want %v (treated as a failure)", got, bastion.StateOpen)
	}

	clock.Advance(10 * time.Second)
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("Execute() after the Goexit-caused Open = %v, want nil (a fresh probe is admitted normally, slot not leaked)", err)
	}
}

// ADR-0010: the admission hook calling runtime.Goexit is a harder case than
// it panicking, since nothing lets Execute's caller "resume" after a
// Goexit -- the calling goroutine simply terminates once its defers run. What
// bastion still guarantees is that its OWN shared state does not leak: a
// later call, from a different goroutine, must not be stuck behind the
// window this Goexit interrupted.
func TestExecute_AdmissionHookGoexitDoesNotLeakTheSlot(t *testing.T) {
	clock := newFakeClock()
	goexitOnTransition := false
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				if goexitOnTransition && ev.To == bastion.StateHalfOpen {
					runtime.Goexit()
				}
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	goexitOnTransition = true
	opRan := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			opRan = true
			return 42, nil
		})
	}()
	<-done
	goexitOnTransition = false

	if opRan {
		t.Fatal("op ran despite the admission hook calling Goexit before it")
	}
	if got := b.State(); got != bastion.StateHalfOpen {
		t.Fatalf("State() after the admission hook's Goexit = %v, want %v (window undecided, slot freed, not leaked)", got, bastion.StateHalfOpen)
	}
	if _, err := bastion.Execute(context.Background(), b, succeedingOp); err != nil {
		t.Fatalf("Execute() after the Goexit = %v, want nil (a fresh probe is still admitted)", err)
	}
}

// FR-09, IR-02: OnStateChange fires synchronously with the transition, and
// carries the breaker's Name so one handler can serve every breaker in a
// process.
func TestExecute_FiresOnStateChange(t *testing.T) {
	clock := newFakeClock()
	var events []bastion.StateChangeEvent
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
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

	if _, err := bastion.Execute(context.Background(), b, failingOp); err == nil {
		t.Fatal("setup: expected the call to fail")
	}

	if len(events) != 1 {
		t.Fatalf("got %d OnStateChange events, want 1: %+v", len(events), events)
	}
	if events[0].From != bastion.StateClosed || events[0].To != bastion.StateOpen {
		t.Fatalf("event = %+v, want From=Closed To=Open", events[0])
	}
	if events[0].Name != "dep" {
		t.Fatalf("event.Name = %q, want %q", events[0].Name, "dep")
	}
}

// A nil Hooks field is a no-op, not a panic -- every field may be nil
// independently (hooks.go).
func TestExecute_NilHooksAreANoOp(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	if _, err := bastion.Execute(context.Background(), b, failingOp); err == nil {
		t.Fatal("setup: expected the call to fail")
	}
	// No WithHooks was given -- Hooks{} zero value, every field nil. Reaching
	// this line without a panic is the assertion.
}

// OnCall fires with Counted true for an admitted call, and OnReject fires
// with the specific rejection reason for a refused one -- State on both
// reports the state the call was admitted or refused under.
func TestExecute_FiresOnCallAndOnReject(t *testing.T) {
	clock := newFakeClock()
	var calls []bastion.CallEvent
	var rejects []bastion.RejectEvent
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnCall:   func(_ context.Context, ev bastion.CallEvent) { calls = append(calls, ev) },
			OnReject: func(_ context.Context, ev bastion.RejectEvent) { rejects = append(rejects, ev) },
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	if _, err := bastion.Execute(context.Background(), b, failingOp); err == nil {
		t.Fatal("setup: expected the call to fail and open the circuit")
	}
	_, _ = bastion.Execute(context.Background(), b, succeedingOp) // rejected: Open

	if len(calls) != 1 {
		t.Fatalf("got %d OnCall events, want 1: %+v", len(calls), calls)
	}
	if !calls[0].Counted || calls[0].State != bastion.StateClosed {
		t.Fatalf("call event = %+v, want Counted=true State=Closed", calls[0])
	}
	if len(rejects) != 1 {
		t.Fatalf("got %d OnReject events, want 1: %+v", len(rejects), rejects)
	}
	if !errors.Is(rejects[0].Reason, bastion.ErrOpenState) || rejects[0].State != bastion.StateOpen {
		t.Fatalf("reject event = %+v, want Reason=ErrOpenState State=Open", rejects[0])
	}
}

// A caller-supplied classifier (WithIsFailure) can excuse an error so it does
// not count against the threshold -- the wiring Execute already does, ahead
// of B2's decision about what the *default* classifier is (there is none yet;
// see WithIsFailure's own godoc).
func TestExecute_HonorsACallerSuppliedClassifier(t *testing.T) {
	clock := newFakeClock()
	notFound := errors.New("404: not found")
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithClock(clock),
		bastion.WithIsFailure(func(err error) bool {
			return !errors.Is(err, notFound) // a 404 is an answer, not a failure
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	_, gotErr := bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
		return 0, notFound
	})
	if !errors.Is(gotErr, notFound) {
		t.Fatalf("Execute() error = %v, want a match for notFound (still returned to the caller)", gotErr)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after an excused error = %v, want %v (the classifier said it is not a failure)", got, bastion.StateClosed)
	}
}

// hooks.go: "An error the classifier excused... arrive[s] here with Counted
// false." Checked directly, not just inferred from State() staying Closed.
func TestExecute_OnCallReportsCountedFalseForAnExcusedError(t *testing.T) {
	clock := newFakeClock()
	notFound := errors.New("404: not found")
	var calls []bastion.CallEvent
	b, err := bastion.New("dep",
		bastion.WithClock(clock),
		bastion.WithIsFailure(func(err error) bool { return !errors.Is(err, notFound) }),
		bastion.WithHooks(bastion.Hooks{
			OnCall: func(_ context.Context, ev bastion.CallEvent) { calls = append(calls, ev) },
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
		return 0, notFound
	})

	if len(calls) != 1 {
		t.Fatalf("got %d OnCall events, want 1: %+v", len(calls), calls)
	}
	if calls[0].Counted {
		t.Fatalf("call event = %+v, want Counted=false for an excused error", calls[0])
	}
}

// CallEvent.Err for a panicking call carries a synthesized error describing
// the panic, not nil -- the field is typed error and a recovered value is
// not one. The caller, separately, still gets the exact original value via
// the re-raised panic (TestExecute_PanicIsReRaisedUnchanged).
func TestExecute_OnCallCarriesASyntheticErrorForAPanic(t *testing.T) {
	clock := newFakeClock()
	var calls []bastion.CallEvent
	b, err := bastion.New("dep",
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnCall: func(_ context.Context, ev bastion.CallEvent) { calls = append(calls, ev) },
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	func() {
		defer func() { _ = recover() }()
		_, _ = bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
			panic("boom")
		})
	}()

	if len(calls) != 1 {
		t.Fatalf("got %d OnCall events, want 1: %+v", len(calls), calls)
	}
	if calls[0].Err == nil || !calls[0].Counted {
		t.Fatalf("call event = %+v, want a non-nil Err and Counted=true", calls[0])
	}
}

// This is a smoke test proving the admission/completion design is race-free
// under go test -race in the broad, mixed-traffic case. It makes no
// assertion about the breaker's end state -- the three tests below it do,
// each targeting one specific concurrency claim of NFR-01.
func TestExecute_ConcurrentCallsDoNotRace(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(3), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = bastion.Execute(context.Background(), b, failingOp)
			} else {
				_, _ = bastion.Execute(context.Background(), b, succeedingOp)
			}
			_ = b.State()
		}(i)
	}
	wg.Wait()
}

// NFR-01: many goroutines racing the exact instant a threshold of 1 is
// reached must still transition the circuit exactly once. This stresses
// complete's "the state has since moved on" guard specifically -- with 50
// goroutines released simultaneously and FailureThreshold(1), every one of
// them is admitted while the circuit is still Closed (they all read
// b.state == StateClosed before any of them has completed), so all 50
// concurrently race to be the one that flips it to Open. Exactly one
// OnStateChange event must fire; a duplicated or lost transition would show
// up as a count other than 1, not as a panic or a race.
func TestExecute_ConcurrentCallsAcrossAStateTransition(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var events []bastion.StateChangeEvent
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
			},
		}),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	const goroutines = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // released together, to maximize contention at admission
			_, _ = bastion.Execute(context.Background(), b, failingOp)
		}()
	}
	close(start)
	wg.Wait()

	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after %d concurrent failures with FailureThreshold(1) = %v, want %v", goroutines, got, bastion.StateOpen)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("got %d OnStateChange events, want exactly 1 (a transition counted twice, or lost and recovered by a second call, both show up here): %+v", len(events), events)
	}
	if events[0].From != bastion.StateClosed || events[0].To != bastion.StateOpen {
		t.Fatalf("event = %+v, want From=Closed To=Open", events[0])
	}
}

// NFR-01: State() must be safe to call concurrently with the writes that
// transition the breaker it reads. The state-transition tests already prove
// State() is *correct*; this proves it is safe to call from a goroutine that
// is not also the one driving Execute -- go test -race is the actual
// assertion here, since State()'s possible return values are already
// exhaustively enumerated by State's own type.
func TestExecute_ConcurrentStateReadsDuringATransition(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(5), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 10 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = b.State()
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for range 50 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, _ = bastion.Execute(context.Background(), b, failingOp)
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
}

// NFR-01: "the half-open allowance holds under concurrent probes -- this is
// where an off-by-one admits two calls where it promised one." 20 goroutines
// released simultaneously against WithHalfOpenMaxCalls(3): each op sleeps
// briefly before returning success, long enough that every goroutine's
// admission decision -- a fast, non-blocking mutex operation -- has already
// happened before any of the admitted probes completes. Exactly 3 must be
// admitted (the real operation invoked, a nil error), and the remaining 17
// must be rejected with ErrTooManyRequests without the operation running at
// all.
func TestExecute_HalfOpenAllowanceHoldsUnderConcurrentProbes(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(10*time.Second),
		bastion.WithHalfOpenMaxCalls(3),
		bastion.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	_, _ = bastion.Execute(context.Background(), b, failingOp)
	clock.Advance(10 * time.Second)

	const goroutines = 20
	var realOpInvocations, admitted, rejected int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := bastion.Execute(context.Background(), b, func(context.Context) (int, error) {
				atomic.AddInt64(&realOpInvocations, 1)
				time.Sleep(30 * time.Millisecond)
				return 42, nil
			})
			switch {
			case err == nil:
				atomic.AddInt64(&admitted, 1)
			case errors.Is(err, bastion.ErrTooManyRequests):
				atomic.AddInt64(&rejected, 1)
			default:
				t.Errorf("Execute() error = %v, want nil or a match for ErrTooManyRequests", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&realOpInvocations); got != 3 {
		t.Fatalf("the real operation ran %d times, want exactly 3 (WithHalfOpenMaxCalls)", got)
	}
	if got := atomic.LoadInt64(&admitted); got != 3 {
		t.Fatalf("%d goroutines were admitted, want exactly 3", got)
	}
	if got := atomic.LoadInt64(&rejected); got != goroutines-3 {
		t.Fatalf("%d goroutines were rejected, want exactly %d", got, goroutines-3)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() after a successful probe resolved the window = %v, want %v", got, bastion.StateClosed)
	}
}

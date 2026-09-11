package bastion_test

import (
	"context"
	"errors"
	"sync"
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

// TODO(B4): New must reject a non-positive threshold, open timeout and
// half-open allowance. Each rejection gets a case here, and each is written
// against a New that does not yet check — seen red, then made green.

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

// TODO(B6): the full concurrency suite of NFR-01 and the overhead
// benchmarks of NFR-02 (breaker_bench_test.go). This is a smoke test proving
// the admission/completion design is race-free under go test -race, not a
// substitute for it -- it makes no assertion about the breaker's end state,
// only that concurrent access to it never races.
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

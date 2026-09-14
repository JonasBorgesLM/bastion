package bastion_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// The runtime counterpart of the struct-field check above: Retry with a
// zero-value policy actually invokes op exactly once, not zero and not more.
func TestRetry_ZeroValuePolicyCallsOpExactlyOnce(t *testing.T) {
	calls := 0
	_, err := bastion.Retry(context.Background(), bastion.RetryPolicy{}, func(context.Context) (int, error) {
		calls++
		return 0, errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Retry() error = %v, want a match for errBoom", err)
	}
	if calls != 1 {
		t.Fatalf("op invoked %d times for the zero-value policy, want 1", calls)
	}
}

func TestRetry_SuccessfulFirstAttemptDoesNotRetry(t *testing.T) {
	calls := 0
	got, err := bastion.Retry(context.Background(), bastion.RetryPolicy{MaxAttempts: 5}, func(context.Context) (int, error) {
		calls++
		return 42, nil
	})
	if err != nil || got != 42 {
		t.Fatalf("Retry() = (%d, %v), want (42, nil)", got, err)
	}
	if calls != 1 {
		t.Fatalf("op invoked %d times after an immediate success, want 1", calls)
	}
}

// Attempts stop at MaxAttempts, and the LAST attempt's own error -- not the
// first, not a generic wrapper -- reaches the caller.
func TestRetry_AttemptsStopAtMaxAttemptsAndTheLastErrorReachesTheCaller(t *testing.T) {
	errs := []error{errors.New("err 1"), errors.New("err 2"), errors.New("err 3")}
	calls := 0
	_, err := bastion.Retry(context.Background(), bastion.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}, func(context.Context) (int, error) {
		e := errs[calls]
		calls++
		return 0, e
	})
	if calls != 3 {
		t.Fatalf("op invoked %d times, want 3", calls)
	}
	if !errors.Is(err, errs[2]) {
		t.Fatalf("Retry() error = %v, want a match for the 3rd attempt's own error (%v)", err, errs[2])
	}
}

// Every field of RetryPolicy that can be silently wrong (issue #21) is
// rejected before op runs at all, not clamped into something plausible-looking
// -- fail visibly rather than degrade quietly, per the README's own design
// principle.
func TestRetry_RejectsAnInvalidPolicyWithoutInvokingOp(t *testing.T) {
	tests := []struct {
		name   string
		policy bastion.RetryPolicy
	}{
		{"negative MaxAttempts", bastion.RetryPolicy{MaxAttempts: -1}},
		{"negative BaseDelay", bastion.RetryPolicy{BaseDelay: -time.Millisecond}},
		{"negative MaxDelay", bastion.RetryPolicy{MaxDelay: -time.Millisecond}},
		{"MaxDelay below BaseDelay", bastion.RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: 50 * time.Millisecond}},
		{"Jitter below 0", bastion.RetryPolicy{Jitter: -0.1}},
		{"Jitter above 1", bastion.RetryPolicy{Jitter: 1.1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			_, err := bastion.Retry(context.Background(), tt.policy, func(context.Context) (int, error) {
				calls++
				return 0, nil
			})
			if !errors.Is(err, bastion.ErrInvalidConfig) {
				t.Fatalf("Retry() error = %v, want a match for ErrInvalidConfig", err)
			}
			if calls != 0 {
				t.Fatalf("op invoked %d times against an invalid policy, want 0", calls)
			}
		})
	}
}

// A policy right at the boundary -- MaxDelay exactly equal to BaseDelay -- is
// valid, not rejected. Only a MaxDelay strictly below BaseDelay is incoherent.
func TestRetry_AcceptsMaxDelayEqualToBaseDelay(t *testing.T) {
	calls := 0
	_, err := bastion.Retry(context.Background(), bastion.RetryPolicy{
		MaxAttempts: 1,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    10 * time.Millisecond,
	}, func(context.Context) (int, error) {
		calls++
		return 42, nil
	})
	if err != nil {
		t.Fatalf("Retry() error = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("op invoked %d times, want 1", calls)
	}
}

// FR-06: the wait is a timer raced against ctx, never time.Sleep. A context
// cancelled mid-wait must make Retry return at once with ctx's own error, not
// wait out the configured delay first and not return the previous attempt's
// error.
func TestRetry_ContextCancelledDuringTheWaitReturnsAtOnceWithCtxsOwnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := bastion.Retry(ctx, bastion.RetryPolicy{MaxAttempts: 2, BaseDelay: 300 * time.Millisecond}, func(context.Context) (int, error) {
		return 0, errBoom
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry() error = %v, want a match for context.Canceled", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Retry() took %s to return after cancellation, want well under the configured 300ms delay", elapsed)
	}
}

// An already-cancelled context, present before the wait between the last
// allowed attempt would even be reached, is caught by the same mechanism: the
// second (final) attempt's own wait check sees ctx already Done.
func TestRetry_AlreadyCancelledContextStopsBeforeTheNextWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	_, err := bastion.Retry(ctx, bastion.RetryPolicy{MaxAttempts: 3, BaseDelay: 0}, func(context.Context) (int, error) {
		calls++
		return 0, errBoom
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry() error = %v, want a match for context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("op invoked %d times against an already-cancelled context, want exactly 1 (the first attempt still runs; the wait before a second does not)", calls)
	}
}

// The opposite case: BaseDelay 0 (no wait needed at all) with a context that
// is NOT cancelled must still let every attempt through -- a zero delay is
// not itself a reason to stop.
func TestRetry_ZeroDelayWithALiveContextRunsEveryAttempt(t *testing.T) {
	calls := 0
	_, err := bastion.Retry(context.Background(), bastion.RetryPolicy{MaxAttempts: 3, BaseDelay: 0}, func(context.Context) (int, error) {
		calls++
		if calls == 3 {
			return 42, nil
		}
		return 0, errBoom
	})
	if err != nil {
		t.Fatalf("Retry() error = %v, want nil (the 3rd attempt succeeds)", err)
	}
	if calls != 3 {
		t.Fatalf("op invoked %d times, want 3", calls)
	}
}

// Integration-level proof that Retry's loop is actually wired to nextDelay
// and a real timer -- nextDelay's own exact math is covered by
// retry_internal_test.go; this only checks the shape survives the real wait.
// A one-sided (lower-bound-only) tolerance absorbs CI scheduling noise
// without risking a flaky upper-bound failure.
func TestRetry_DelaysRoughlyDoubleBetweenAttempts(t *testing.T) {
	var timestamps []time.Time
	_, _ = bastion.Retry(context.Background(), bastion.RetryPolicy{
		MaxAttempts: 4,
		BaseDelay:   15 * time.Millisecond,
		MaxDelay:    100 * time.Millisecond,
	}, func(context.Context) (int, error) {
		timestamps = append(timestamps, time.Now())
		return 0, errBoom
	})

	if len(timestamps) != 4 {
		t.Fatalf("got %d attempts, want 4", len(timestamps))
	}
	want := []time.Duration{15 * time.Millisecond, 30 * time.Millisecond, 60 * time.Millisecond}
	for i, w := range want {
		gap := timestamps[i+1].Sub(timestamps[i])
		if gap < w*8/10 {
			t.Fatalf("gap before attempt %d = %s, want at least ~%s", i+2, gap, w)
		}
	}
}

// ADR-0011, issue #47: reproduces the reported defect directly. A rejected
// attempt on a NON-final try must skip both the remaining attempts and the
// backoff that would have preceded them -- the exact claim ADR-0006 made and
// v0.1.0 did not keep.
//
// Negative control: verified failing (elapsed ~= 1.4s, the full three-wait
// schedule, and realCalls == 1 only by coincidence of the OLD code still
// eventually giving up at MaxAttempts) against a version of Retry with the
// `if !retriable(err) { return zero, err }` check removed -- confirmed by
// deliberately removing it and observing this test time out its bound before
// restoring the check.
func TestRetry_SkipsTheWaitAfterABreakerRejection(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	realCalls := 0
	start := time.Now()
	_, err = bastion.Retry(context.Background(),
		bastion.RetryPolicy{MaxAttempts: 4, BaseDelay: 200 * time.Millisecond},
		func(ctx context.Context) (int, error) {
			return bastion.Execute(ctx, b, func(context.Context) (int, error) {
				realCalls++
				return 0, errBoom
			})
		},
	)
	elapsed := time.Since(start)

	if !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("Retry() error = %v, want a match for ErrOpenState", err)
	}
	if realCalls != 1 {
		t.Fatalf("the real operation ran %d times, want 1 (attempts 2-4 must be short-circuited once the circuit is seen open)", realCalls)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Retry() took %s, want well under the full 1.4s backoff schedule (200+400+800ms) -- attempts after the rejection must not wait", elapsed)
	}
}

// ADR-0011: a caller-supplied IsRetriable can retry through a rejection --
// the wait DOES happen (proving the override genuinely re-enables it), even
// though the breaker itself still refuses the underlying call each time.
func TestRetry_IsRetriableOverrideCanRetryThroughARejection(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(1), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	realCalls := 0
	start := time.Now()
	_, err = bastion.Retry(context.Background(),
		bastion.RetryPolicy{
			MaxAttempts: 3,
			BaseDelay:   50 * time.Millisecond,
			IsRetriable: func(error) bool { return true },
		},
		func(ctx context.Context) (int, error) {
			return bastion.Execute(ctx, b, func(context.Context) (int, error) {
				realCalls++
				return 0, errBoom
			})
		},
	)
	elapsed := time.Since(start)

	if !errors.Is(err, bastion.ErrOpenState) {
		t.Fatalf("Retry() error = %v, want a match for ErrOpenState (the last attempt's own rejection)", err)
	}
	if realCalls != 1 {
		t.Fatalf("the real operation ran %d times, want 1 (attempts 2 and 3 are still rejected by the breaker itself; the override only keeps Retry looping through the rejection, it does not make Execute admit the call)", realCalls)
	}
	// Two waits (before attempts 2 and 3) must have happened: 50+100=150ms,
	// against a lower bound with headroom for scheduling noise.
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Retry() took %s, want at least ~150ms (the override must still wait between attempts)", elapsed)
	}
}

// The composition test that matters more than the rest of this file (issue
// #22): both orders are exercised with the SAME RetryPolicy and the SAME
// FailureThreshold, so the difference in outcome is attributable only to the
// nesting order, not to a different configuration.
//
// Recommended order (ADR-0006): Retry wraps Execute. Each attempt is its own,
// individually-admitted-and-counted call. FailureThreshold(2) with 3 failing
// attempts means the breaker opens after attempt 2, and attempt 3 -- which
// Retry still tries, since ADR-0011's retriability check is skipped on
// whatever attempt is already the last one (there being no further wait or
// attempt left to save) -- gets ErrOpenState instead of reaching the real
// operation. The real operation therefore runs only twice, and the breaker
// ends Open. ADR-0011 changes nothing about this specific test: the
// rejection here happens to land exactly on the final attempt, which is
// exactly the one case the retriability check never gets to shortcut.
func TestRetryBreakerComposition_RecommendedOrder(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(2), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	realCalls := 0
	_, err = bastion.Retry(context.Background(),
		bastion.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond},
		func(ctx context.Context) (int, error) {
			return bastion.Execute(ctx, b, func(context.Context) (int, error) {
				realCalls++
				return 0, errBoom
			})
		},
	)
	if err == nil {
		t.Fatal("Retry() error = nil, want the final attempt's rejection or failure")
	}
	if realCalls != 2 {
		t.Fatalf("the real operation ran %d times, want 2 (the breaker opens after 2 and short-circuits the 3rd retry attempt)", realCalls)
	}
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() = %v, want %v", got, bastion.StateOpen)
	}
}

// Rejected order, exercised for contrast (ADR-0006's "alternative that was
// rejected"): Execute wraps Retry. The entire retry sequence runs inside ONE
// admitted call, so the breaker sees a single outcome no matter how many real
// attempts happened underneath. With the SAME FailureThreshold(2) and the
// SAME 3-attempt policy, all 3 real attempts run (nothing inside the retry
// loop knows the breaker exists), and the breaker counts exactly ONE failure
// -- below its threshold of 2 -- so it stays Closed.
func TestRetryBreakerComposition_RejectedOrderForComparison(t *testing.T) {
	clock := newFakeClock()
	b, err := bastion.New("dep", bastion.WithFailureThreshold(2), bastion.WithClock(clock))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	realCalls := 0
	_, err = bastion.Execute(context.Background(), b, func(ctx context.Context) (int, error) {
		return bastion.Retry(ctx,
			bastion.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond},
			func(context.Context) (int, error) {
				realCalls++
				return 0, errBoom
			},
		)
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want the retry loop's exhausted error")
	}
	if realCalls != 3 {
		t.Fatalf("the real operation ran %d times, want 3 (the whole retry sequence is invisible to the breaker)", realCalls)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("State() = %v, want %v (one counted failure is below FailureThreshold(2))", got, bastion.StateClosed)
	}
}

package bastion

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// RetryPolicy describes how a failed call is retried: how many attempts, how
// long to wait between them, and how much of that wait is randomised (FR-06).
//
// It is a plain struct rather than a set of functional options, and the
// asymmetry with [Breaker] is intentional. A Breaker is constructed once,
// validated, and lives for the process; a policy is a small value a call site
// states inline, where a literal with named fields reads better than four
// With-calls. The zero value retries nothing, so a policy nobody configured
// does not silently add attempts.
type RetryPolicy struct {
	// MaxAttempts bounds the total number of attempts, the first one
	// included. Zero or one means no retry.
	MaxAttempts int

	// BaseDelay is the wait before the second attempt. Each subsequent wait
	// doubles it, up to MaxDelay.
	BaseDelay time.Duration

	// MaxDelay caps the exponential growth. Zero means uncapped, which on a
	// long retry budget is how a "transient" failure turns into a request
	// that hangs for minutes.
	MaxDelay time.Duration

	// Jitter is the fraction of each delay that is randomised away, in
	// [0, 1]. A computed delay of d is reduced by a random amount in
	// [0, d*Jitter], so the actual wait falls in [d*(1-Jitter), d]. Zero
	// means none — and none is the setting that synchronises every client of
	// a recovering service into one thundering herd, which is the failure
	// retry was supposed to prevent.
	Jitter float64

	// IsRetriable reports whether err is worth retrying. nil (the default)
	// retries every error except a rejection from a [Breaker]'s [Execute] —
	// [ErrOpenState] or [ErrTooManyRequests] — since retrying immediately
	// after either wastes the backoff wait on a call that was never going to
	// reach the dependency (ADR-0011). Set a non-nil function to retry
	// through a rejection anyway, or to also exclude other permanent errors
	// (a 400, say) from the retry budget.
	IsRetriable func(error) bool
}

// validate reports whether p describes anything [Retry] can act on. Checked
// once, up front, so a misconfigured policy fails before the first attempt
// rather than producing a confusing delay or a silently-constant wait.
func (p RetryPolicy) validate() error {
	switch {
	case p.MaxAttempts < 0:
		return fmt.Errorf("%w: RetryPolicy.MaxAttempts must not be negative, got %d", ErrInvalidConfig, p.MaxAttempts)
	case p.BaseDelay < 0:
		return fmt.Errorf("%w: RetryPolicy.BaseDelay must not be negative, got %s", ErrInvalidConfig, p.BaseDelay)
	case p.MaxDelay < 0:
		return fmt.Errorf("%w: RetryPolicy.MaxDelay must not be negative, got %s", ErrInvalidConfig, p.MaxDelay)
	case p.MaxDelay > 0 && p.MaxDelay < p.BaseDelay:
		return fmt.Errorf("%w: RetryPolicy.MaxDelay (%s) must be zero or at least BaseDelay (%s)", ErrInvalidConfig, p.MaxDelay, p.BaseDelay)
	case p.Jitter < 0 || p.Jitter > 1:
		return fmt.Errorf("%w: RetryPolicy.Jitter must be in [0, 1], got %v", ErrInvalidConfig, p.Jitter)
	}
	return nil
}

// overflowGuard is a duration comfortably below where doubling it again would
// wrap a time.Duration (an int64 count of nanoseconds). It exists only to stop
// nextDelay's loop from ever computing a negative or wrapped-around duration
// on a policy with an unreasonably long attempt chain; ordinary policies
// (delays measured in milliseconds to minutes, attempts in the single or low
// double digits) never come close to it.
const overflowGuard = time.Duration(1) << 61

// nextDelay computes the wait before the given attempt (the attempt about to
// be made; attempt 2 is the first retry, matching BaseDelay's own godoc: "the
// wait before the second attempt"). It is a pure function of p and attempt,
// except for the jitter draw, which uses math/rand, not crypto/rand — a
// scheduling decision, not a secret, so no CSPRNG cost is spent on a hot
// retry path. gosec's G404 flags any math/rand use on principle; it is
// suppressed at the call site with the reasoning above, not silenced
// globally.
//
// Doubling is done iteratively with a clamp at every step, rather than by
// shifting attempt-2 bits at once, so a policy with a very large MaxAttempts
// can never compute an overflowed or negative duration: as soon as the
// running value would cross MaxDelay (or overflowGuard, absent a MaxDelay),
// growth stops.
func nextDelay(p RetryPolicy, attempt int) time.Duration {
	shift := max(attempt-2, 0)

	d := p.BaseDelay
	for range shift {
		if p.MaxDelay > 0 && d >= p.MaxDelay {
			d = p.MaxDelay
			break
		}
		if d > overflowGuard {
			if p.MaxDelay > 0 {
				d = p.MaxDelay
			} else {
				d = overflowGuard
			}
			break
		}
		d *= 2
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}

	if p.Jitter > 0 && d > 0 {
		d -= time.Duration(float64(d) * p.Jitter * rand.Float64()) // #nosec G404 -- scheduling jitter, not a secret; see the Jitter field's own doc.
	}
	return d
}

// sleepRespectingContext waits for d or until ctx is done, whichever comes
// first. A zero or negative d still checks ctx once rather than skipping the
// wait unconditionally: a context already done when a retry attempt finishes
// must stop the loop even when there is nothing left to wait out.
func sleepRespectingContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Retry calls op, retrying on error per p, and returns the first success or
// the last attempt's error once p.MaxAttempts is reached, or the first
// attempt whose error p.IsRetriable rejects (FR-06).
//
// Retry has no notion of a [Breaker] and touches none of a Breaker's counters
// — it composes with one entirely by nesting at the call site, and the
// library's own recommendation (docs/adr/0006-retry-composes-around-the-breaker-not-inside-it.md)
// is to nest [Execute] *inside* the retried operation, so each attempt is
// independently admitted and counted:
//
//	result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (T, error) {
//		return bastion.Execute(ctx, breaker, realOp)
//	})
//
// See that ADR for why, and for the real cost of the choice: a
// FailureThreshold set low relative to MaxAttempts can open the circuit
// before one logical call's own retry budget is exhausted, on purpose — the
// threshold counts real attempts against the dependency, not logical calls.
//
// An attempt whose error [RetryPolicy.IsRetriable] rejects stops the loop
// immediately — no further attempt, and no wait beforehand — rather than
// spending the remaining budget and backoff schedule on a call already known
// not to be worth repeating (ADR-0011). The default rejects exactly a
// rejection from a Breaker's own Execute ([ErrOpenState],
// [ErrTooManyRequests]): recognizing those two sentinel *values* is a
// materially weaker coupling than Retry referencing a `*Breaker`, which it
// still never does.
//
// The wait between attempts is a timer raced against ctx, never time.Sleep: a
// context cancelled mid-wait makes Retry return at once, with ctx's own
// error — not the previous attempt's — since the caller stopped waiting on
// this altogether rather than merely watching one more attempt fail (FR-05's
// spirit, applied here independently of any breaker).
//
// Retry does not inspect or special-case ctx before the very first attempt;
// op receives it unchanged, exactly as [Execute] does, and is free to derive
// its own child context per attempt
// (docs/adr/0007-no-dedicated-timeout-helper.md) if a per-attempt timeout is
// wanted.
//
// RetryPolicy has no field bounding the total time a call to Retry may take;
// wrap the whole call in [context.WithTimeout] instead:
//
//	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
//	defer cancel()
//	result, err := bastion.Retry(ctx, policy, op)
//
// Because a context deadline is cooperative rather than preemptive, this
// bounds the loop the way a caller asking for a total elapsed-time budget
// actually wants: no further attempt starts once ctx is Done — the same
// check that already stops the loop on cancellation, reused here — but an
// attempt already in flight when the deadline passes is not forcibly cut
// off; it runs to completion unless op itself watches ctx and returns early
// (docs/adr/0012-retrys-total-elapsed-time-is-bounded-by-the-callers-context.md).
//
// Retry returns [ErrInvalidConfig] without invoking op at all if p does not
// describe a policy it can act on.
func Retry[T any](ctx context.Context, p RetryPolicy, op func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := p.validate(); err != nil {
		return zero, err
	}

	maxAttempts := max(p.MaxAttempts, 1)
	retriable := p.IsRetriable
	if retriable == nil {
		retriable = defaultIsRetriable
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := op(ctx)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		if !retriable(err) {
			return zero, err
		}
		if werr := sleepRespectingContext(ctx, nextDelay(p, attempt+1)); werr != nil {
			return zero, werr
		}
	}
	return zero, lastErr
}

// defaultIsRetriable is used when p.IsRetriable is nil (ADR-0011): every
// error is retriable except a rejection from a Breaker's own Execute, which
// never reached the dependency and is not going to reach it on a retry
// either.
func defaultIsRetriable(err error) bool {
	return !errors.Is(err, ErrOpenState) && !errors.Is(err, ErrTooManyRequests)
}

package bastion

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

// nextDelay is pure (except for the jitter draw), so these tests exercise it
// directly rather than through Retry's real timer -- no real waiting, no
// flakiness budget spent on scheduling noise. Retry's own wiring (that it
// actually calls sleepRespectingContext with nextDelay's result) is covered
// separately in retry_test.go's black-box suite.

func TestNextDelay_FirstRetryIsBaseDelay(t *testing.T) {
	p := RetryPolicy{BaseDelay: 10 * time.Millisecond}
	if got := nextDelay(p, 2); got != 10*time.Millisecond {
		t.Fatalf("nextDelay(attempt=2) = %s, want %s", got, 10*time.Millisecond)
	}
}

func TestNextDelay_DoublesEachAttempt(t *testing.T) {
	p := RetryPolicy{BaseDelay: 10 * time.Millisecond}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{2, 10 * time.Millisecond},
		{3, 20 * time.Millisecond},
		{4, 40 * time.Millisecond},
		{5, 80 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := nextDelay(p, tt.attempt); got != tt.want {
			t.Errorf("nextDelay(attempt=%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestNextDelay_CapsAtMaxDelay(t *testing.T) {
	p := RetryPolicy{BaseDelay: 10 * time.Millisecond, MaxDelay: 35 * time.Millisecond}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{2, 10 * time.Millisecond}, // below the cap, untouched
		{3, 20 * time.Millisecond}, // below the cap, untouched
		{4, 35 * time.Millisecond}, // would be 40ms uncapped
		{5, 35 * time.Millisecond}, // stays capped
	}
	for _, tt := range tests {
		if got := nextDelay(p, tt.attempt); got != tt.want {
			t.Errorf("nextDelay(attempt=%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestNextDelay_ZeroJitterIsExact(t *testing.T) {
	p := RetryPolicy{BaseDelay: 10 * time.Millisecond, Jitter: 0}
	want := nextDelay(p, 3)
	for range 20 {
		if got := nextDelay(p, 3); got != want {
			t.Fatalf("nextDelay with Jitter=0 varied across calls: got %s, want %s every time", got, want)
		}
	}
}

// Every draw must fall in [d*(1-Jitter), d], per the Jitter field's own
// documented formula, and at least one draw across many must differ from the
// unjittered maximum -- otherwise nothing here proves jitter runs at all.
func TestNextDelay_JitterStaysWithinItsDocumentedBand(t *testing.T) {
	p := RetryPolicy{BaseDelay: 100 * time.Millisecond, Jitter: 0.5}
	const full = 100 * time.Millisecond
	floor := full / 2 // full * (1 - 0.5)

	sawBelowFull := false
	for range 500 {
		got := nextDelay(p, 2)
		if got < floor || got > full {
			t.Fatalf("nextDelay = %s, want in [%s, %s]", got, floor, full)
		}
		if got < full {
			sawBelowFull = true
		}
	}
	if !sawBelowFull {
		t.Fatal("500 draws never differed from the unjittered delay; jitter does not appear to be applied")
	}
}

// Jitter is applied after capping, not before -- a capped delay must still be
// reducible by jitter. Jitter=1 (maximum) on a delay well past its cap: if
// jitter ran on the PRE-cap value instead, it could still land anywhere in
// [0, uncappedDelay], including above the cap, which this test also forbids.
func TestNextDelay_JitterAppliesAfterCapping(t *testing.T) {
	p := RetryPolicy{BaseDelay: 10 * time.Millisecond, MaxDelay: 15 * time.Millisecond, Jitter: 1}
	const cap_ = 15 * time.Millisecond // attempt 5 would be 80ms uncapped

	sawBelowCap := false
	for range 200 {
		got := nextDelay(p, 5)
		if got < 0 {
			t.Fatalf("nextDelay produced a negative duration: %s", got)
		}
		if got > cap_ {
			t.Fatalf("nextDelay = %s, want at most the cap %s even with Jitter=1", got, cap_)
		}
		if got < cap_ {
			sawBelowCap = true
		}
	}
	if !sawBelowCap {
		t.Fatal("200 draws with Jitter=1 on a capped delay never went below the cap")
	}
}

// Regression test for the overflow guard: a very long attempt chain with no
// MaxDelay must still return a positive, sane duration rather than wrapping
// past time.Duration's int64 range into something negative or absurdly small.
//
// Negative control: verified failing (a negative duration) against a version
// of nextDelay that computed d := p.BaseDelay << shift directly instead of
// doubling iteratively with a clamp at every step.
func TestNextDelay_NeverOverflowsOnALongAttemptChain(t *testing.T) {
	p := RetryPolicy{BaseDelay: time.Millisecond}
	if got := nextDelay(p, 1000); got <= 0 {
		t.Fatalf("nextDelay(attempt=1000) = %s, want a positive duration", got)
	}
}

// The overflow guard's other branch: growth crosses overflowGuard while a
// MaxDelay is set (and the MaxDelay itself is large enough that the ordinary
// "d >= p.MaxDelay" clamp never fires first) -- the result must still be
// exactly MaxDelay, not overflowGuard's own fallback value.
func TestNextDelay_OverflowGuardYieldsToAnExplicitMaxDelay(t *testing.T) {
	p := RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: overflowGuard * 2}
	if got := nextDelay(p, 1000); got != p.MaxDelay {
		t.Fatalf("nextDelay(attempt=1000) = %s, want exactly MaxDelay (%s)", got, p.MaxDelay)
	}
}

// FuzzNextDelay covers the input space the overflow regression test above
// found by hand (issue #59): four caller-controlled numbers, invariants easy
// to state and easy to break. Seeded from the table-driven cases above.
//
// Inputs are constrained to what RetryPolicy.validate would accept -- the
// only shape nextDelay is ever actually called with, from Retry's own loop
// -- rather than fuzzed as arbitrary values validate already rejects
// elsewhere. This keeps every invariant below meaningful for the real
// calling contract instead of asserting something about states nextDelay
// can never actually be called in.
//
// This is how nextDelay's overflow-clamp fix earned its own negative
// control, not a hand-mutated one: the very first real 30s fuzzing run
// found seed 8dfa42327e276ab3 (now saved in testdata/fuzz/FuzzNextDelay/)
// failing against nextDelay as it stood before that fix -- BaseDelay=10ms,
// attempt 40 exceeding overflowGuard while attempt 41 clamped back down to
// it, a transient non-monotonic spike (see nextDelay's own doc comment for
// why). Confirmed failing against the pre-fix shape, confirmed passing
// after, and this seed now replays on every ordinary `go test` run
// regardless of -fuzz, so that specific defect can never silently return.
func FuzzNextDelay(f *testing.F) {
	f.Add(int64(10*time.Millisecond), int64(0), 0.0, 2)
	f.Add(int64(10*time.Millisecond), int64(35*time.Millisecond), 0.0, 4)
	f.Add(int64(100*time.Millisecond), int64(0), 0.5, 2)
	f.Add(int64(10*time.Millisecond), int64(15*time.Millisecond), 1.0, 5)
	f.Add(int64(time.Millisecond), int64(0), 0.0, 1000)
	f.Add(int64(time.Millisecond), int64(0), 0.0, 1) // shift == 0, the loop body never runs

	f.Fuzz(func(t *testing.T, baseDelayNs, maxDelayNs int64, jitter float64, attempt int) {
		switch {
		case baseDelayNs < 0, maxDelayNs < 0, attempt < 1:
			t.Skip()
		case maxDelayNs > 0 && maxDelayNs < baseDelayNs:
			t.Skip()
		case jitter < 0 || jitter > 1 || math.IsNaN(jitter):
			t.Skip()
		}

		p := RetryPolicy{
			BaseDelay: time.Duration(baseDelayNs),
			MaxDelay:  time.Duration(maxDelayNs),
			Jitter:    jitter,
		}
		got := nextDelay(p, attempt)

		if got < 0 {
			t.Fatalf("nextDelay(%+v, %d) = %s, want never negative", p, attempt, got)
		}
		if p.MaxDelay > 0 && got > p.MaxDelay {
			t.Fatalf("nextDelay(%+v, %d) = %s, want at most MaxDelay %s", p, attempt, got, p.MaxDelay)
		}

		if p.Jitter == 0 {
			// Deterministic: the jitter draw is the only non-pure part of
			// nextDelay, so with it switched off, calling again must
			// reproduce exactly.
			if got2 := nextDelay(p, attempt); got2 != got {
				t.Fatalf("nextDelay(%+v, %d) is not deterministic with Jitter=0: %s vs %s", p, attempt, got, got2)
			}
			// Monotonically non-decreasing in attempt until the cap. Jitter
			// randomizes each draw independently, so this is only checkable
			// with it off -- a later attempt's smaller random reduction
			// could otherwise make a strictly-growing schedule look like it
			// went backwards without the growth itself being wrong.
			if attempt < math.MaxInt {
				if next := nextDelay(p, attempt+1); next < got {
					t.Fatalf("nextDelay(%+v, %d) = %s > nextDelay(%+v, %d) = %s, want non-decreasing", p, attempt, got, p, attempt+1, next)
				}
			}
		}
	})
}

// ADR-0011: defaultIsRetriable, tested directly since it is unexported and
// has no other observable surface than the behavior Retry's own black-box
// tests already exercise end to end.
func TestDefaultIsRetriable(t *testing.T) {
	ordinary := errors.New("boom")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"an ordinary error is retriable", ordinary, true},
		{"ErrOpenState is not retriable", ErrOpenState, false},
		{"ErrTooManyRequests is not retriable", ErrTooManyRequests, false},
		{"a wrapped ErrOpenState is not retriable", fmt.Errorf("wrapped: %w", ErrOpenState), false},
		{"a wrapped ErrTooManyRequests is not retriable", fmt.Errorf("wrapped: %w", ErrTooManyRequests), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultIsRetriable(tt.err); got != tt.want {
				t.Errorf("defaultIsRetriable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

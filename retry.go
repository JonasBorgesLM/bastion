package bastion

import "time"

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

	// Jitter is the fraction of each delay that is randomised, in [0, 1].
	// Zero means none — and none is the setting that synchronises every
	// client of a recovering service into one thundering herd, which is the
	// failure retry was supposed to prevent.
	Jitter float64
}

// TODO(B3): the retry loop of FR-06, and the timeout of FR-07.
//
// Retry composes with a [Breaker] and never requires one. The order matters and
// belongs in an ADR — the "retry inside the breaker or composed around it"
// question in docs/adr/README.md. Retry *outside* the breaker means a retried
// burst can open the circuit, retry *inside* means the breaker sees one call
// where several were made. The library takes no position by coupling them
// — it takes one by documenting which composition it recommends and why.
//
// The shape under consideration:
//
//	func Retry[T any](ctx context.Context, p RetryPolicy, op func(context.Context) (T, error)) (T, error)
//
// Two things this must get right, both of which have tests before code:
//
//   - The delay is waited out against ctx, never with time.Sleep. A cancelled
//     context must abandon the wait immediately rather than at the end of it,
//     and it must not be counted as a failure (FR-05).
//   - Jitter is drawn from math/rand, not crypto/rand. It is a scheduling
//     decision, not a secret, and a CSPRNG in a hot retry path is cost without
//     a threat to spend it on.
//
// FR-07 also lands here, and it may not need code at all: a per-operation
// timeout is context.WithTimeout, and a helper wrapping two lines of standard
// library earns its place only if it removes a mistake callers actually make.
// Decide that before writing it.

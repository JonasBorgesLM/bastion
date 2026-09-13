package bastion

import (
	"fmt"
	"time"
)

// Provisional defaults. TODO(B4): each of these is a number nobody has
// justified yet. They are written down here rather than scattered through New
// so that the argument about them happens in one place, and they are settled by
// the ADR that closes the "static or adaptive failure threshold" question in
// docs/adr/README.md, before v1.
const (
	defaultFailureThreshold = 5
	defaultOpenTimeout      = 30 * time.Second
	defaultHalfOpenMaxCalls = 1
)

// options accumulates [Option] values before [New] validates them and freezes
// them into a [Breaker].
type options struct {
	failureThreshold int
	openTimeout      time.Duration
	halfOpenMaxCalls int
	isFailure        func(error) bool
	clock            Clock
	hooks            Hooks
}

// validate reports whether o describes anything [New] can build a [Breaker]
// from. Checked once, before construction, so a misconfigured breaker fails
// visibly there rather than misbehaving — or, for a nil Clock, panicking on
// its first call — later and less obviously (IR-04).
func (o options) validate() error {
	switch {
	case o.failureThreshold <= 0:
		return fmt.Errorf("%w: WithFailureThreshold must be positive, got %d", ErrInvalidConfig, o.failureThreshold)
	case o.openTimeout <= 0:
		return fmt.Errorf("%w: WithOpenTimeout must be positive, got %s", ErrInvalidConfig, o.openTimeout)
	case o.halfOpenMaxCalls <= 0:
		return fmt.Errorf("%w: WithHalfOpenMaxCalls must be positive, got %d", ErrInvalidConfig, o.halfOpenMaxCalls)
	case o.clock == nil:
		return fmt.Errorf("%w: WithClock must not be given a nil Clock", ErrInvalidConfig)
	}
	return nil
}

// Option configures a [Breaker] at construction. See [New].
type Option func(*options)

// WithFailureThreshold sets how many consecutive failures close the circuit
// into [StateOpen] (FR-02). Must be positive; [New] returns [ErrInvalidConfig]
// otherwise.
func WithFailureThreshold(n int) Option {
	return func(o *options) { o.failureThreshold = n }
}

// WithOpenTimeout sets how long the circuit stays in [StateOpen] before a probe
// call is admitted (FR-01). The elapsed time is evaluated on the next call, so
// nothing happens at the instant the timeout expires and no goroutine is
// waiting for it. Must be positive; [New] returns [ErrInvalidConfig] otherwise.
func WithOpenTimeout(d time.Duration) Option {
	return func(o *options) { o.openTimeout = d }
}

// WithHalfOpenMaxCalls bounds how many probe calls [StateHalfOpen] admits
// before further calls are rejected with [ErrTooManyRequests] (FR-01). Must be
// positive; [New] returns [ErrInvalidConfig] otherwise.
func WithHalfOpenMaxCalls(n int) Option {
	return func(o *options) { o.halfOpenMaxCalls = n }
}

// WithIsFailure sets the classifier that decides whether an error counts
// against the circuit (FR-04). It is the injection point for a host's own error
// taxonomy: an HTTP 404 or a validation error is an answer from a healthy
// dependency, and counting it as a failure opens a circuit on a working
// service.
//
// The default, used when no classifier is set, counts every non-nil error as
// a failure. WithIsFailure is never consulted for a call whose context was
// Done when the operation returned — that case counts as neither a success
// nor a failure regardless of what fn would have said, decided before fn is
// ever reached (FR-05, ADR-0005).
func WithIsFailure(fn func(error) bool) Option {
	return func(o *options) { o.isFailure = fn }
}

// WithClock substitutes the [Clock] the breaker reads (NFR-05). The default is
// [SystemClock]. Tests pass a fake so that a transition is caused by advancing
// time rather than by waiting for it. c must not be nil; [New] returns
// [ErrInvalidConfig] rather than building a breaker that would panic on its
// first call.
func WithClock(c Clock) Option {
	return func(o *options) { o.clock = c }
}

// WithHooks sets the observability handlers (FR-09). Handlers run synchronously
// on the calling goroutine and must not block (IR-02).
func WithHooks(h Hooks) Option {
	return func(o *options) { o.hooks = h }
}

// TODO(B5): the fallback of FR-08 is not an Option, and the reason is worth
// recording rather than rediscovering. A fallback produces the call's return
// value, so it is typed T — and a Go method cannot introduce a type parameter
// its receiver does not already have. Configuring it on the Breaker would force
// either a Breaker[T], which defeats one named breaker guarding a dependency
// called from several call sites with several return types, or an interface{}
// round trip, which is what generics are here to avoid. The likely answer is
// that the fallback is an argument at the call site, where its type is known.
// It needs an ADR before B5, not a decision made in passing.

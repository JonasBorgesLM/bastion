package bastion

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Breaker guards calls to one remote dependency. Construct one with [New];
// there are no exported setters, so a Breaker's configuration is fixed for its
// lifetime once New returns (IR-03).
//
// A Breaker is safe for concurrent use (NFR-01). Several breakers in one
// process are independent and share nothing — one per dependency is the
// intended shape, not one per process (FR-10).
type Breaker struct {
	name string

	failureThreshold int
	openTimeout      time.Duration
	halfOpenMaxCalls int
	isFailure        func(error) bool
	clock            Clock
	hooks            Hooks

	mu sync.Mutex

	state State

	// consecutiveFailures counts toward failureThreshold while state is
	// Closed (FR-02). Reset to zero by any success, and by leaving Closed.
	consecutiveFailures int

	// openedAt is when state most recently became Open — the anchor
	// effectiveState reads against openTimeout to decide whether a Half-Open
	// probe is due (FR-01, NFR-05).
	openedAt time.Time

	// halfOpenInFlight counts probes admitted and not yet completed, bounded
	// by halfOpenMaxCalls.
	halfOpenInFlight int

	// halfOpenAdmittedAt is when the current Half-Open window opened — set
	// exactly once per window, the instant the first probe is admitted. It is
	// the anchor for ADR-0004's lease: a window whose allowance has been full
	// for longer than openTimeout is presumed stuck on a probe that will never
	// return, and is reopened rather than left rejecting forever.
	halfOpenAdmittedAt time.Time

	// halfOpenGeneration identifies the current Half-Open window. Every probe
	// admitted into a window is stamped with its generation; a probe whose
	// generation no longer matches when it completes belongs to a window the
	// breaker has already moved on from (by lease expiry, or because a
	// sibling probe already resolved the window), and its outcome is
	// discarded rather than applied (ADR-0004).
	halfOpenGeneration int
}

// New returns a Breaker named name, configured by opts.
//
// The name is required and appears in every event the hooks emit (FR-09), so a
// single handler can serve every breaker in a process. It is an identifier for
// an operator, not a key: New registers nothing anywhere, and two breakers
// sharing a name are still two independent breakers (IR-03).
//
// New returns [ErrInvalidConfig] rather than panicking on a configuration it
// will not build from.
func New(name string, opts ...Option) (*Breaker, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: a breaker must be named", ErrInvalidConfig)
	}

	o := options{
		failureThreshold: defaultFailureThreshold,
		openTimeout:      defaultOpenTimeout,
		halfOpenMaxCalls: defaultHalfOpenMaxCalls,
		clock:            SystemClock{},
	}
	for _, opt := range opts {
		opt(&o)
	}

	// TODO(B4): validate the accumulated options — a non-positive threshold, a
	// non-positive open timeout, a non-positive half-open allowance. Each
	// rejection needs a test, and a test for a rejection is only verified once
	// it has been seen failing against a New that does not check.

	return &Breaker{
		name:             name,
		failureThreshold: o.failureThreshold,
		openTimeout:      o.openTimeout,
		halfOpenMaxCalls: o.halfOpenMaxCalls,
		isFailure:        o.isFailure,
		clock:            o.clock,
		hooks:            o.hooks,
		state:            StateClosed,
	}, nil
}

// Name returns the breaker's name, as given to [New] (FR-10).
func (b *Breaker) Name() string { return b.name }

// State returns the circuit's current state (FR-13), including a timeout that
// has already elapsed: reading State never leaves a stale answer for a caller
// who has not yet made another call.
//
// State never mutates the breaker and never fires a hook. The transition it
// computes here is persisted, and reported to [Hooks.OnStateChange], only when
// [Execute] next actually admits or refuses a call — reading the current state
// has no side effect to fire a hook *with*, since Hooks take a
// [context.Context] that a bare read does not have one of.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.effectiveState(b.clock.Now())
}

// effectiveState computes what State() should report right now, without
// mutating any field. Call with b.mu held.
func (b *Breaker) effectiveState(now time.Time) State {
	switch b.state {
	case StateOpen:
		if now.Sub(b.openedAt) >= b.openTimeout {
			return StateHalfOpen
		}
		return StateOpen

	case StateHalfOpen:
		if b.halfOpenInFlight >= b.halfOpenMaxCalls &&
			now.Sub(b.halfOpenAdmittedAt) >= b.openTimeout {
			// ADR-0004: the outstanding probe(s) have overstayed their lease.
			return StateOpen
		}
		return StateHalfOpen

	default: // StateClosed
		return StateClosed
	}
}

// admission is what admit decides for one call: whether it is admitted, and
// if so, whether it is a Half-Open probe (and under which generation) so its
// eventual completion can be attributed correctly, or discarded as stale.
type admission struct {
	admitted   bool
	isProbe    bool
	generation int
	state      State // the state the call was admitted or refused under
	reject     error
	transition *StateChangeEvent // non-nil if admitting this call itself moved the state
}

// admit decides whether a call is let through, mutating and persisting any
// transition that decision depends on. It never invokes caller code and it
// never fires a hook — Execute does both, after releasing b.mu.
//
// The transition, if any, is decided by effectiveState — the same function
// State reads — rather than by a second, independently written time
// comparison. Two comparisons of the same boundary is how a State() that
// disagrees with Execute() about whether a timeout has elapsed gets shipped;
// one comparison, read from two call sites, cannot drift from itself.
func (b *Breaker) admit(now time.Time) admission {
	b.mu.Lock()
	defer b.mu.Unlock()

	eff := b.effectiveState(now)
	var transition *StateChangeEvent
	if eff != b.state {
		from := b.state
		b.state = eff
		b.halfOpenGeneration++
		b.halfOpenInFlight = 0
		if eff == StateOpen {
			// ADR-0004: HalfOpen's window overstayed its lease. This call is
			// refused as Open, exactly like any other Open rejection; the next
			// call after a fresh openTimeout becomes the next window's probe.
			b.openedAt = now
		}
		// eff == StateHalfOpen (Open's timeout elapsed): halfOpenAdmittedAt is
		// left unset here and stamped below, by whichever branch actually
		// admits this call as the window's first probe.
		transition = &StateChangeEvent{Name: b.name, From: from, To: eff}
	}

	switch b.state {
	case StateClosed:
		return admission{admitted: true, state: StateClosed, transition: transition}

	case StateOpen:
		return admission{admitted: false, state: StateOpen, reject: ErrOpenState, transition: transition}

	default: // StateHalfOpen
		if b.halfOpenInFlight < b.halfOpenMaxCalls {
			if b.halfOpenInFlight == 0 {
				b.halfOpenAdmittedAt = now
			}
			b.halfOpenInFlight++
			return admission{
				admitted: true, isProbe: true, generation: b.halfOpenGeneration,
				state: StateHalfOpen, transition: transition,
			}
		}
		return admission{
			admitted: false, state: StateHalfOpen, reject: ErrTooManyRequests,
			transition: transition,
		}
	}
}

// outcome classifies a completed call for the breaker's own bookkeeping. It
// never affects what Execute returns to the caller — op's own value and
// error reach the caller unchanged on every outcome (ADR-0001) — it only
// decides what complete does with the counters.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeFailure
	// outcomeCancelled reports that ctx (the exact context passed into
	// Execute) was Done when op returned. It moves neither counter (FR-05,
	// ADR-0005): not the failure count, and not a reset of it either — a
	// cancelled call is not evidence the dependency is healthy.
	outcomeCancelled
)

// complete records the outcome of a call admit previously admitted, mutating
// and persisting any transition it causes. Call with adm.admitted true; adm
// and now come from admit and clock.Now() respectively, at the call site.
func (b *Breaker) complete(now time.Time, adm admission, oc outcome) *StateChangeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	if adm.isProbe {
		if adm.generation != b.halfOpenGeneration {
			// ADR-0004: this window was already invalidated — by a lease
			// expiring, by a sibling probe already resolving it
			// (WithHalfOpenMaxCalls > 1), or by any other departure from this
			// window. Every place that moves b.state away from StateHalfOpen
			// also increments halfOpenGeneration in the same critical section
			// (see admit's eff != b.state block, and the increment just below
			// this one) — so a generation match here is proof, not a
			// coincidence, that b.state is still StateHalfOpen and this
			// completion is the window's own to resolve.
			return nil
		}
		b.halfOpenInFlight--
		if oc == outcomeCancelled {
			// ADR-0005: the slot is freed so a later call can still be
			// admitted as a probe, but the window's own verdict stays
			// undecided — this probe never actually told the breaker
			// anything about the dependency. Generation is deliberately not
			// bumped: a sibling probe (WithHalfOpenMaxCalls > 1) can still
			// resolve this same window normally, and if none ever does,
			// ADR-0004's staleness lease is what eventually reopens it.
			return nil
		}
		from := b.state
		if oc == outcomeFailure {
			b.state = StateOpen
			b.openedAt = now
		} else {
			b.state = StateClosed
			b.consecutiveFailures = 0
		}
		b.halfOpenInFlight = 0
		b.halfOpenGeneration++ // this window is resolved; any other sibling is now stale too
		return &StateChangeEvent{Name: b.name, From: from, To: b.state}
	}

	// Admitted while Closed. If the state has since moved on — another
	// concurrent call already tripped the breaker — this completion is noise
	// against a period that is no longer current, and touches nothing.
	if b.state != StateClosed {
		return nil
	}
	switch oc {
	case outcomeCancelled:
		return nil
	case outcomeSuccess:
		b.consecutiveFailures = 0
		return nil
	default: // outcomeFailure
		b.consecutiveFailures++
		if b.consecutiveFailures < b.failureThreshold {
			return nil
		}
		b.state = StateOpen
		b.openedAt = now
		b.consecutiveFailures = 0
		return &StateChangeEvent{Name: b.name, From: StateClosed, To: StateOpen}
	}
}

// classify reports whether err should count against the failure threshold.
// Uses the caller-supplied classifier if one was set via [WithIsFailure];
// otherwise every non-nil error counts. classify is not consulted at all when
// the call's context was cancelled (ADR-0005) — that path is decided in
// Execute before classify is ever reached.
func (b *Breaker) classify(err error) bool {
	if b.isFailure != nil {
		return b.isFailure(err)
	}
	return err != nil
}

// fireStateChange calls Hooks.OnStateChange if one is set (IR-02: nil is a
// no-op, and this is always called with b.mu released).
func (b *Breaker) fireStateChange(ctx context.Context, ev *StateChangeEvent) {
	if ev == nil || b.hooks.OnStateChange == nil {
		return
	}
	b.hooks.OnStateChange(ctx, *ev)
}

// Execute runs op through b: admitted immediately in [StateClosed], admitted
// as a bounded probe in [StateHalfOpen], and refused without invoking op in
// [StateOpen] — returning [ErrOpenState] or [ErrTooManyRequests] (ADR-0001).
//
// op's own return value and error reach the caller unchanged; Execute wraps,
// retypes, or annotates neither. A panic in op is recovered, counted as a
// failure unconditionally, and re-raised with its original value once the
// breaker's bookkeeping is complete (FR-11, ADR-0003) — b.mu is never held
// while op runs, so a panicking op cannot leave it locked.
//
// If ctx is Done when op returns, the call counts as neither a success nor a
// failure (FR-05, ADR-0005): the failure count is untouched — not reset
// either, since a cancelled call is not evidence the dependency is healthy —
// and a Half-Open probe's window stays undecided rather than resolved. This
// is decided by reading ctx.Err() on the exact context value passed to
// Execute, never by matching op's returned error: an operation whose own
// internally-derived context times out on its own can return
// context.DeadlineExceeded too, and that is an ordinary failure, not caller
// cancellation.
//
// Hooks fire synchronously, on the calling goroutine, always after b.mu has
// been released (FR-09, IR-02): a transition caused by admitting the call,
// then op runs, then [Hooks.OnCall] or [Hooks.OnReject], then a transition
// caused by the call's outcome, if any.
//
// If op is being invoked as a probe into a dependency that may still be
// unavailable, it should itself respect ctx's deadline where one is set. A
// probe that never returns cannot strand the circuit (FR-12, ADR-0004), but a
// bounded probe recovers on its own without ever admitting a second,
// overlapping one.
func Execute[T any](ctx context.Context, b *Breaker, op func(context.Context) (T, error)) (T, error) {
	var zero T

	adm := b.admit(b.clock.Now())
	b.fireStateChange(ctx, adm.transition)

	if !adm.admitted {
		if b.hooks.OnReject != nil {
			b.hooks.OnReject(ctx, RejectEvent{Name: b.name, State: adm.state, Reason: adm.reject})
		}
		return zero, adm.reject
	}

	result, panicked, recovered, err := callSafely(ctx, op)

	var oc outcome
	switch {
	case panicked:
		// ADR-0003: unconditional, even if ctx also happens to be Done — a
		// panic is never evidence that the caller gave up.
		oc = outcomeFailure
	case ctx.Err() != nil:
		// ADR-0005: read on ctx itself, the exact value passed into Execute —
		// never on err, which cannot tell caller cancellation apart from an
		// operation's own internally-derived context timing out.
		oc = outcomeCancelled
	case b.classify(err):
		oc = outcomeFailure
	default:
		oc = outcomeSuccess
	}

	completion := b.complete(b.clock.Now(), adm, oc)
	b.fireStateChange(ctx, completion)

	if b.hooks.OnCall != nil {
		callErr := err
		if panicked {
			// FR-11: the caller gets the original recovered value via panic()
			// below, unchanged. This is only what the hook sees — CallEvent.Err
			// is typed error, and a recovered value is not one.
			callErr = fmt.Errorf("bastion: operation panicked: %v", recovered)
		}
		b.hooks.OnCall(ctx, CallEvent{Name: b.name, State: adm.state, Err: callErr, Counted: oc == outcomeFailure})
	}

	if panicked {
		panic(recovered)
	}
	return result, err
}

// callSafely runs op, recovering a panic instead of letting it unwind through
// the breaker's own bookkeeping. b.mu is never involved here — this is called
// only after admit has already released it.
func callSafely[T any](ctx context.Context, op func(context.Context) (T, error)) (result T, panicked bool, recovered any, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			recovered = r
		}
	}()
	result, err = op(ctx)
	return result, false, nil, err
}

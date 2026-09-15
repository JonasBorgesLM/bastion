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
	if err := o.validate(); err != nil {
		return nil, err
	}

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

// Counts is a snapshot of a Breaker's current bookkeeping, taken under the
// same lock every call already uses (FR-13, NFR-01). It answers what the
// [Hooks] event stream cannot — what this breaker's state looks like right
// now — not what has happened over time, which a host already gets exactly
// by counting [Hooks.OnCall] and [Hooks.OnReject] events
// (docs/adr/0015-counts-answers-only-what-the-hook-stream-cannot.md).
type Counts struct {
	// State is the breaker's current state, including a timeout already
	// elapsed — the same value [Breaker.State] would return, computed once
	// so Counts is an internally consistent snapshot rather than several
	// separately-read fields.
	State State

	// ConsecutiveFailures counts toward FailureThreshold. Zero unless State
	// is [StateClosed] (FR-02): a probe or a rejection is not itself a
	// consecutive failure, and this field is not "how many failures led to
	// the current state," only "how many more would trip it from here."
	ConsecutiveFailures int

	// FailureThreshold is the value given to [WithFailureThreshold] (or its
	// default), included so a caller holding only a Counts value — not the
	// options [New] was given — can compute how close ConsecutiveFailures is
	// to tripping the circuit without needing to have kept that number
	// itself.
	FailureThreshold int

	// OpenedAt is when the current Open episode began. Zero unless State is
	// [StateOpen] — not "the last time this breaker was Open," which would
	// stay stale and misleading long after it recovered.
	OpenedAt time.Time
}

// Counts returns a snapshot of b's current bookkeeping (FR-09, FR-13).
//
// Every field is already-stored state — Counts adds nothing to Execute's
// hot path, no new field on Breaker and no new write in admit or complete
// (docs/adr/0015-counts-answers-only-what-the-hook-stream-cannot.md).
func (b *Breaker) Counts() Counts {
	b.mu.Lock()
	defer b.mu.Unlock()
	eff := b.effectiveState(b.clock.Now())
	c := Counts{
		State:               eff,
		ConsecutiveFailures: b.consecutiveFailures,
		FailureThreshold:    b.failureThreshold,
	}
	if eff == StateOpen {
		c.OpenedAt = b.openedAt
	}
	return c
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

// runHookSafely calls fn, recovering any panic instead of letting it
// propagate immediately. Every hook call site in Execute uses this, so one
// hook's panic can never prevent bastion's own bookkeeping — or a sibling
// hook — from running (ADR-0010). The panic value, if any, is returned so the
// caller can decide what to do with it.
func runHookSafely(fn func()) (panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	fn()
	return nil
}

// callOptions accumulates [CallOption] values for a single call to [Execute].
// Deliberately empty — no option is defined yet (ADR-0014). It exists so a
// future option is a compatible addition: a new field here and a new
// WithXxx constructor, never a change to Execute's own signature.
type callOptions struct{}

// CallOption customizes a single call to [Execute]. No option is defined
// yet; see docs/adr/0014-execute-gains-an-empty-call-option-slot.md for why
// the slot exists anyway. callOptions is unexported, so no caller outside
// this package can construct a non-nil CallOption today — passing none is
// the only thing to do with this parameter until a WithXxx constructor is
// added.
type CallOption func(*callOptions)

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
// caused by the call's outcome, if any. A panic in a hook is recovered and
// re-raised to this call's own caller only once bastion's own bookkeeping for
// this call is fully resolved — it can never leave a Half-Open slot admitted
// and never released, and it can never prevent op from being invoked or a
// sibling hook from running (ADR-0010). If op itself also panics, op's panic
// is what reaches the caller (ADR-0003's unconditional priority); a hook's
// panic in that case is resolved but not the one re-raised.
//
// If a hook prevents op from ever being invoked at all — by panicking, or by
// calling runtime.Goexit, before op is reached — that call is accounted like
// a cancelled context: neither a failure nor a success, since the breaker has
// no evidence about the dependency, only that its own hook broke
// (ADR-0010). The same accounting applies if op itself calls runtime.Goexit
// without returning: bastion cannot tell that apart from op's own panic, so
// it is treated the same way, unconditionally, as FR-11 already treats one.
//
// If op is being invoked as a probe into a dependency that may still be
// unavailable, it should itself respect ctx's deadline where one is set. A
// probe that never returns cannot strand the circuit (FR-12, ADR-0004), but a
// bounded probe recovers on its own without ever admitting a second,
// overlapping one.
//
// opts is reserved for future per-call options (ADR-0014); no option is
// defined yet, and every call site today correctly passes none.
func Execute[T any](
	ctx context.Context, b *Breaker, op func(context.Context) (T, error),
	opts ...CallOption,
) (T, error) {
	var o callOptions
	for _, opt := range opts {
		opt(&o)
	}

	var zero T

	adm := b.admit(b.clock.Now())

	if !adm.admitted {
		var hookPanic any
		capture := func(p any) {
			if p != nil && hookPanic == nil {
				hookPanic = p
			}
		}
		capture(runHookSafely(func() { b.fireStateChange(ctx, adm.transition) }))
		if b.hooks.OnReject != nil {
			capture(runHookSafely(func() {
				b.hooks.OnReject(ctx, RejectEvent{Name: b.name, State: adm.state, Reason: adm.reject})
			}))
		}
		if hookPanic != nil {
			panic(hookPanic)
		}
		return zero, adm.reject
	}

	var (
		result      T
		opErr       error
		opStarted   bool // true once Execute has begun calling op
		opRan       bool // true once op has returned normally (not via panic or Goexit)
		opPanicked  bool
		opRecovered any
		hookPanic   any
	)
	capture := func(p any) {
		if p != nil && hookPanic == nil {
			hookPanic = p
		}
	}

	func() {
		// Registered before the admission hook or op run, so admit's mutation
		// of shared state (incrementing halfOpenInFlight for a probe) is
		// always matched by a complete call — on a normal return, a panic
		// anywhere in this closure, or a runtime.Goexit anywhere in it.
		// Defers run on Goexit; the rest of this closure's code, and
		// everything in Execute after the call to it, does not (ADR-0010).
		defer func() {
			if r := recover(); r != nil {
				if opStarted {
					opPanicked = true
					opRecovered = r
				} else {
					capture(r) // the admission hook panicked before op was ever attempted
				}
			}

			var oc outcome
			switch {
			case !opStarted:
				oc = outcomeCancelled
			case opPanicked:
				oc = outcomeFailure
			case !opRan:
				// op was invoked but never returned normally: it called
				// runtime.Goexit. Treated the same as an ordinary panic
				// (FR-11) — bastion cannot tell a caller bug from
				// dependency-triggered corruption either way, and an
				// operation that never completed is not evidence the
				// dependency is healthy.
				oc = outcomeFailure
			case ctx.Err() != nil:
				oc = outcomeCancelled
			case b.classify(opErr):
				oc = outcomeFailure
			default:
				oc = outcomeSuccess
			}

			completion := b.complete(b.clock.Now(), adm, oc)
			capture(runHookSafely(func() { b.fireStateChange(ctx, completion) }))

			if b.hooks.OnCall != nil {
				callErr := opErr
				switch {
				case !opStarted:
					callErr = fmt.Errorf("bastion: a hook panicked before the operation could run")
				case opPanicked:
					// FR-11: the caller gets the original recovered value via
					// panic() below, unchanged. This is only what the hook
					// sees — CallEvent.Err is typed error, and a recovered
					// value is not one.
					callErr = fmt.Errorf("bastion: operation panicked: %v", opRecovered)
				}
				capture(runHookSafely(func() {
					b.hooks.OnCall(ctx, CallEvent{Name: b.name, State: adm.state, Err: callErr, Counted: oc == outcomeFailure})
				}))
			}
		}()

		b.fireStateChange(ctx, adm.transition)
		opStarted = true
		result, opErr = op(ctx)
		opRan = true
	}()

	switch {
	case opPanicked:
		panic(opRecovered) // ADR-0003: unconditional priority over any hook panic
	case hookPanic != nil:
		panic(hookPanic)
	}
	return result, opErr
}

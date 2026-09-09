package bastion

import (
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

	mu    sync.Mutex
	state State
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

// State returns the circuit's current state (FR-13).
//
// TODO(B1): this must evaluate the lazy Open to Half-Open transition before
// answering, or it reports Open for a circuit whose timeout expired and which
// the next call would admit. Reading a stale answer here is the kind of bug
// that only shows up in someone's dashboard.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// TODO(B1): the entry point, and the counters it maintains.
//
// The shape under consideration, and the reason it is a function rather than a
// method: a method cannot introduce a type parameter, so a generic result on a
// non-generic Breaker has to arrive this way. See the architecture decisions in
// REQUIREMENTS.md.
//
//	func Execute[T any](ctx context.Context, b *Breaker, op func(context.Context) (T, error)) (T, error)
//
// The name is not settled — Execute against Do against Run — and neither is the
// signature. Both are the "entry point name and signature" question in
// docs/adr/README.md, and both get an ADR before this is written, because a
// library's entry point is the one thing that cannot be renamed after release
// without breaking every caller.
//
// The state this needs, deliberately not declared until the transitions that
// own it are: the consecutive-failure count (FR-02), the instant the circuit
// opened (FR-01), and the number of probe calls admitted in Half-Open. All of
// them live under b.mu.
//
// Two things this must not get wrong, both of them silent:
//
//   - Every acquisition of b.mu around caller-supplied code is released by
//     defer (FR-11). A panic through a held lock does not surface as a panic;
//     it surfaces as every later call to this dependency blocking forever.
//   - The Half-Open allowance must be recoverable (FR-12). A probe that never
//     returns takes the last slot with it, and the circuit then rejects
//     everything for good while reporting itself as recovering.

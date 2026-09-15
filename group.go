package bastion

import (
	"fmt"
	"sync"
)

// Group manages one [Breaker] per key, created on first use (FR-10). It is
// the shape almost every real consumer needs — one breaker per downstream
// host, per tenant, per endpoint — without each consumer writing the same
// map, mutex, and double-checked get-or-create by hand.
//
// A Group a caller constructs and holds is an ordinary value with an
// ordinary lifetime, exactly like a *Breaker itself — not the package-level
// registry IR-03 forbids. Two different Groups share nothing, and "two
// breakers sharing a name are still two independent breakers" still holds:
// a Group only means one caller chose to have one *Breaker answer to one
// key (docs/adr/0019-group-bounds-growth-with-a-required-cap-not-eviction.md).
//
// A Group is safe for concurrent use (NFR-01). Concurrent [Group.Get] calls
// for the same not-yet-seen key create exactly one *Breaker; every caller
// observes the same instance.
//
// A Group has an observability cost alongside its memory one: every member is
// named after its key, and that name is in every event the hooks emit (FR-10),
// so a Group keyed per tenant emits a per-tenant attribute. See [Hooks] for
// what to send instead when the key space is large, and SECURITY.md for the
// memory side.
type Group struct {
	mu       sync.Mutex
	breakers map[string]*Breaker
	opts     []Option
	maxKeys  int
}

// NewGroup returns a Group that creates up to maxKeys breakers, each
// configured by opts — the same [Option] values [New] itself takes, applied
// identically to every member.
//
// maxKeys is required and positional, not a default behind an option a
// caller could overlook: an unbounded Group keyed by anything
// attacker-influenced — a tenant id, a Host header — is a memory-exhaustion
// vector reachable from outside
// (docs/adr/0019-group-bounds-growth-with-a-required-cap-not-eviction.md).
// A caller who genuinely wants effectively unbounded growth still has to
// write that number down and mean it.
//
// NewGroup returns [ErrInvalidConfig] if maxKeys is not positive, or for the
// same reasons [New] itself would reject opts — validated once, here,
// rather than deferred to whichever [Group.Get] call happens to trip over
// it first.
func NewGroup(maxKeys int, opts ...Option) (*Group, error) {
	if maxKeys <= 0 {
		return nil, fmt.Errorf("%w: NewGroup maxKeys must be positive, got %d", ErrInvalidConfig, maxKeys)
	}
	if _, err := buildOptions(opts); err != nil {
		return nil, err
	}

	return &Group{
		breakers: make(map[string]*Breaker),
		opts:     opts,
		maxKeys:  maxKeys,
	}, nil
}

// Get returns the Breaker for key, creating one on first use with the
// options given to [NewGroup]. The new Breaker's name is key itself, so it
// is already distinguishable in every hook event without Group adding any
// mechanism of its own (FR-10).
//
// Get returns [ErrGroupFull] without creating anything if key has not been
// seen before and the group already holds MaxKeys breakers
// (docs/adr/0019-group-bounds-growth-with-a-required-cap-not-eviction.md).
// An existing key is always served from the map regardless of how full the
// group is — the cap bounds distinct keys, not calls.
func (g *Group) Get(key string) (*Breaker, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if b, ok := g.breakers[key]; ok {
		return b, nil
	}
	if len(g.breakers) >= g.maxKeys {
		return nil, fmt.Errorf("%w: already holds MaxKeys (%d) breakers", ErrGroupFull, g.maxKeys)
	}

	b, err := New(key, g.opts...)
	if err != nil {
		return nil, err
	}
	g.breakers[key] = b
	return b, nil
}

// Delete removes key from the group, if present, freeing its slot toward
// MaxKeys. A *Breaker a caller already obtained from [Group.Get] remains
// perfectly usable — Delete only affects the group's own map — but a
// subsequent Get for the same key creates a fresh Breaker with no memory of
// the deleted one's accumulated evidence, and it is the caller's own
// responsibility to know that discarding it is correct (a tenant offboarded,
// a host decommissioned), the same way [Breaker.Reset] already requires
// (docs/adr/0019-group-bounds-growth-with-a-required-cap-not-eviction.md).
func (g *Group) Delete(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.breakers, key)
}

// Len returns the number of distinct keys currently held, for comparing
// against MaxKeys — the same reason [Breaker.Counts] exists: a caller can
// poll how close the group is to its own limit without first having to
// catch [ErrGroupFull].
func (g *Group) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.breakers)
}

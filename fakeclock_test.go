package bastion_test

import (
	"sync"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// fakeClock is a bastion.Clock whose time only moves when a test moves it
// (NFR-05). Every state-transition test drives this rather than the wall clock:
// an Open circuit becomes eligible for Half-Open because Advance was called,
// not because the test slept for a second and hoped the machine was not busy.
//
// It is a test double and it lives in a _test.go file on purpose. Exporting one
// from the library would make it API the library then has to keep compatible.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// newFakeClock returns a clock stopped at a fixed, arbitrary instant. The
// instant is in the past and is not time.Now(): a test that accidentally reads
// the real clock somewhere then produces an obviously wrong duration rather
// than a plausible one.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

// Now implements bastion.Clock. It is safe for concurrent use so that the
// race-detector suite of NFR-01 exercises the breaker rather than the double.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Assert at compile time that the double still satisfies the interface. If
// Clock ever gains a method, this fails here rather than in every test that
// passes the double to WithClock.
var _ bastion.Clock = (*fakeClock)(nil)

package bastion

import "time"

// Clock is the library's only source of time (NFR-05). A Breaker reads the
// clock; it never calls time.Now directly and it never sleeps.
//
// The interface has exactly one method because the Breaker needs exactly one
// thing: the current instant, to decide whether an open circuit's timeout has
// elapsed. That decision is made lazily, on the next call, so nothing here
// needs a timer or a goroutine. A retry's delay is the caller's to wait out
// against its own context, which is why Clock has no Sleep or After.
//
// Substituting a Clock is what makes a state-transition test deterministic: an
// Open circuit becomes eligible for Half-Open because the fake clock was
// advanced, not because the test slept and hoped.
type Clock interface {
	// Now returns the current instant. Implementations must be safe for
	// concurrent use (NFR-01).
	Now() time.Time
}

// SystemClock is the default [Clock], reading the real wall clock. It is the
// zero value of a struct rather than a package-level variable so that it holds
// no state and there is nothing global to reconfigure (IR-03).
type SystemClock struct{}

// Now returns time.Now.
func (SystemClock) Now() time.Time { return time.Now() }

// The fake clock the transition tests drive lives in fakeclock_test.go, not
// here. A test double exported from the library is a double the library then
// has to keep compatible.

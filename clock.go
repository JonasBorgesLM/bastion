package bastion

import "time"

// Clock is the library's only source of time (NFR-05). A Breaker reads the
// clock; it never calls time.Now directly and it never sleeps.
//
// The interface has exactly one method because the Breaker needs exactly one
// thing: the current instant, to decide whether an open circuit's timeout has
// elapsed. That decision is made lazily, on the next call, so nothing here
// needs a timer or a goroutine. A retry's delay is the caller's to wait out
// against its own context, which is why Clock has no Sleep or After — see
// [Retry]'s own godoc for why Retry does not go through Clock at all.
//
// Substituting a Clock is what makes a state-transition test deterministic: an
// Open circuit becomes eligible for Half-Open because the fake clock was
// advanced, not because the test slept and hoped.
//
// # Monotonic time
//
// Every transition compares now.Sub(anchor) against a duration — openTimeout
// against the breaker's own openedAt anchor, most directly. [SystemClock] returns time.Now(),
// which on every platform this module targets carries a monotonic reading
// alongside the wall-clock one, and time.Time.Sub prefers it when both
// operands have one. That makes every one of those comparisons immune to a
// wall-clock jump: an NTP correction or a manual clock change cannot make an
// Open circuit reopen early or stay Open for an extra hour. This is a real
// safety property of [SystemClock] specifically, inherited by every Breaker
// that uses it — not something this interface enforces or can enforce, since
// Clock exposes only [time.Time], not a comparison method. A Clock built from
// [time.Date] (as this package's own test double correctly does, deliberately,
// to control time in a test) or from any other wall-clock-only source loses
// this property silently: nothing here will tell a caller their custom Clock
// no longer has it. Anyone implementing Clock for a non-test purpose needs to
// know that before choosing a time source.
//
// # This interface cannot grow a second method
//
// A second method — a timer, an After, anything — is a breaking change for
// every existing [Clock] implementation, [SystemClock] included, the same way
// any interface addition is. Nothing about the current design needs one:
// nothing in [Breaker] sleeps, and [Retry] does not use Clock at all (see
// above). Adding a method here is deliberately not a decision to make lightly
// or by accident — it changes what every implementation, including a host's
// own test doubles, must satisfy.
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

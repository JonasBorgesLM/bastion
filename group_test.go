package bastion_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/JonasBorgesLM/bastion"
)

func TestNewGroup_RejectsNonPositiveMaxKeys(t *testing.T) {
	for _, maxKeys := range []int{0, -1} {
		if _, err := bastion.NewGroup(maxKeys); !errors.Is(err, bastion.ErrInvalidConfig) {
			t.Errorf("NewGroup(%d) error = %v, want a match for ErrInvalidConfig", maxKeys, err)
		}
	}
}

// ADR-0019: opts are validated once, at construction, exactly like New's
// own -- not deferred to whichever Get call happens to trip over them
// first.
func TestNewGroup_RejectsAnInvalidOptionAtConstruction(t *testing.T) {
	_, err := bastion.NewGroup(10, bastion.WithFailureThreshold(-1))
	if !errors.Is(err, bastion.ErrInvalidConfig) {
		t.Fatalf("NewGroup with an invalid option: error = %v, want a match for ErrInvalidConfig", err)
	}
}

// Get reuses New to construct each member, and New itself rejects an empty
// name -- Get inherits that rejection for free rather than duplicating the
// check, since key IS the new Breaker's name.
func TestGroup_GetRejectsAnEmptyKey(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	if _, err := g.Get(""); !errors.Is(err, bastion.ErrInvalidConfig) {
		t.Fatalf("Get(\"\") error = %v, want a match for ErrInvalidConfig", err)
	}
	if got := g.Len(); got != 0 {
		t.Fatalf("Len() after a rejected Get(\"\") = %d, want 0 (nothing must be left behind)", got)
	}
}

func TestGroup_GetCreatesAUsableBreakerOnFirstUse(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}

	b, err := g.Get("dep-a")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	got, err := bastion.Execute(context.Background(), b, succeedingOp)
	if err != nil {
		t.Fatalf("Execute() on a Group-created breaker: error = %v, want nil", err)
	}
	if got != 42 {
		t.Fatalf("Execute() = %d, want 42", got)
	}
}

// The new Breaker's name is the key itself -- already distinguishable in
// hook events with no mechanism of Group's own (FR-10).
func TestGroup_BreakerNameIsTheKey(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	b, err := g.Get("payments-api")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got := b.Name(); got != "payments-api" {
		t.Fatalf("Name() = %q, want %q", got, "payments-api")
	}
}

func TestGroup_GetReturnsTheSameInstanceForTheSameKey(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	first, err := g.Get("dep-a")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	second, err := g.Get("dep-a")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if first != second {
		t.Fatal("Get(\"dep-a\") returned different *Breaker values across two calls, want the same instance")
	}
}

// Two different keys must be two independent breakers -- tripping one must
// not affect the other, the same invariant IR-03 already states for two
// breakers sharing a name.
func TestGroup_DifferentKeysAreIndependentBreakers(t *testing.T) {
	g, err := bastion.NewGroup(10, bastion.WithFailureThreshold(1))
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	a, err := g.Get("dep-a")
	if err != nil {
		t.Fatalf("Get(dep-a) error = %v", err)
	}
	b, err := g.Get("dep-b")
	if err != nil {
		t.Fatalf("Get(dep-b) error = %v", err)
	}

	if _, err := bastion.Execute(context.Background(), a, failingOp); err == nil {
		t.Fatal("setup: expected dep-a's call to fail and open its circuit")
	}
	if got := a.State(); got != bastion.StateOpen {
		t.Fatalf("a.State() = %v, want %v", got, bastion.StateOpen)
	}
	if got := b.State(); got != bastion.StateClosed {
		t.Fatalf("b.State() after only a's circuit tripped = %v, want %v (independent)", got, bastion.StateClosed)
	}
}

// Every member shares one Option set, applied identically (ADR-0019).
func TestGroup_OptionsApplyToEveryMember(t *testing.T) {
	g, err := bastion.NewGroup(10, bastion.WithFailureThreshold(1))
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	b, err := g.Get("dep-a")
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if _, err := bastion.Execute(context.Background(), b, failingOp); err == nil {
		t.Fatal("setup: expected the call to fail")
	}
	if got := b.State(); got != bastion.StateOpen {
		t.Fatalf("State() after 1 failure with WithFailureThreshold(1) = %v, want %v", got, bastion.StateOpen)
	}
}

// ADR-0019's central decision: a hard cap with an explicit error, not
// eviction. An existing key stays servable even once the group is full;
// only a genuinely new key is refused.
//
// Negative control: verified failing (a 3rd distinct breaker created, no
// error) against a version of Get whose cap check used > instead of >=.
func TestGroup_GetReturnsErrGroupFullForANewKeyOnceFull(t *testing.T) {
	g, err := bastion.NewGroup(2)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	if _, err := g.Get("a"); err != nil {
		t.Fatalf("Get(a) error = %v", err)
	}
	if _, err := g.Get("b"); err != nil {
		t.Fatalf("Get(b) error = %v", err)
	}

	if _, err := g.Get("c"); !errors.Is(err, bastion.ErrGroupFull) {
		t.Fatalf("Get(c) on a full group: error = %v, want a match for ErrGroupFull", err)
	}
	if got := g.Len(); got != 2 {
		t.Fatalf("Len() after the refused Get = %d, want 2 (nothing created)", got)
	}

	// An existing key must still be servable from the full group.
	if _, err := g.Get("a"); err != nil {
		t.Fatalf("Get(a) again on a full group: error = %v, want nil (existing key)", err)
	}
}

func TestGroup_DeleteFreesASlotForANewKey(t *testing.T) {
	g, err := bastion.NewGroup(1)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	if _, err := g.Get("a"); err != nil {
		t.Fatalf("Get(a) error = %v", err)
	}
	if _, err := g.Get("b"); !errors.Is(err, bastion.ErrGroupFull) {
		t.Fatalf("Get(b) on a full group: error = %v, want a match for ErrGroupFull", err)
	}

	g.Delete("a")
	if got := g.Len(); got != 0 {
		t.Fatalf("Len() after Delete(a) = %d, want 0", got)
	}
	if _, err := g.Get("b"); err != nil {
		t.Fatalf("Get(b) after Delete(a) freed a slot: error = %v, want nil", err)
	}
}

func TestGroup_DeleteOfAnUnknownKeyIsANoOp(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	g.Delete("never-existed") // must not panic
	if got := g.Len(); got != 0 {
		t.Fatalf("Len() after deleting an unknown key = %d, want 0", got)
	}
}

func TestGroup_LenReflectsDistinctKeysOnly(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}
	if got := g.Len(); got != 0 {
		t.Fatalf("Len() on a fresh group = %d, want 0", got)
	}
	if _, err := g.Get("a"); err != nil {
		t.Fatalf("Get(a) error = %v", err)
	}
	if _, err := g.Get("a"); err != nil { // repeated key, must not grow Len
		t.Fatalf("Get(a) again error = %v", err)
	}
	if _, err := g.Get("b"); err != nil {
		t.Fatalf("Get(b) error = %v", err)
	}
	if got := g.Len(); got != 2 {
		t.Fatalf("Len() after 2 distinct keys (one requested twice) = %d, want 2", got)
	}
}

// issue #56's own explicit "Done when": concurrent creation of the same key
// must produce exactly one Breaker. Every goroutine observing the same
// pointer is what proves that -- if two had been constructed, at least one
// goroutine would have to observe a different pointer than the others.
func TestGroup_ConcurrentGetForTheSameNewKeyCreatesExactlyOneBreaker(t *testing.T) {
	g, err := bastion.NewGroup(10)
	if err != nil {
		t.Fatalf("NewGroup error = %v", err)
	}

	const goroutines = 50
	results := make([]*bastion.Breaker, goroutines)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			b, err := g.Get("shared-key")
			if err != nil {
				t.Errorf("Get error = %v", err)
				return
			}
			results[i] = b
		}(i)
	}
	close(start)
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("results[0] is nil")
	}
	for i, b := range results {
		if b != first {
			t.Fatalf("results[%d] = %p, want the same instance as results[0] (%p)", i, b, first)
		}
	}
	if got := g.Len(); got != 1 {
		t.Fatalf("Len() after concurrent Get on one key = %d, want 1", got)
	}
}

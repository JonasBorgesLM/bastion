package bastion

import (
	"context"
	"testing"
)

// ADR-0014: CallOption's underlying func type takes an unexported
// *callOptions, so no caller outside this package can construct one -- the
// opts-processing loop inside Execute has no externally observable surface
// to exercise until the first real option is added. This is exactly the case
// CLAUDE.md's testing convention reserves internal tests for.
func TestExecute_ProcessesEveryProvidedCallOption(t *testing.T) {
	b, err := New("dep")
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	var firstRan, secondRan bool
	first := CallOption(func(*callOptions) { firstRan = true })
	second := CallOption(func(*callOptions) { secondRan = true })

	got, err := Execute(context.Background(), b, func(context.Context) (int, error) {
		return 42, nil
	}, first, second)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if got != 42 {
		t.Fatalf("Execute() = %d, want 42", got)
	}
	if !firstRan || !secondRan {
		t.Fatalf("firstRan=%v secondRan=%v, want both true -- every provided CallOption must run", firstRan, secondRan)
	}
}

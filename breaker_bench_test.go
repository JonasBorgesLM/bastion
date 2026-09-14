package bastion_test

import (
	"context"
	"testing"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// NFR-02: the overhead is measured, not asserted. Three shapes, so a reader
// can see what a breaker costs relative to no breaker at all, on both the
// path that does real work and the path that does none:
//
//   - BenchmarkExecute_ClosedHappyPath: the cost Execute adds on top of the
//     baseline when every call is admitted and succeeds.
//   - BenchmarkExecute_OpenRejectionPath: the cost of the fast-fail path,
//     which never invokes the operation at all.
//   - BenchmarkOpDirect: the baseline -- the same operation, called with no
//     breaker in front of it.
//
// Run with `go test -bench=. -benchmem ./...` and record ns/op and
// allocs/op somewhere a later change to admit/complete can be compared
// against; this file only shapes what gets measured, not the numbers
// themselves, which drift with the machine that runs them.

func benchOp(context.Context) (int, error) { return 42, nil }

func BenchmarkExecute_ClosedHappyPath(b *testing.B) {
	breaker, err := bastion.New("bench")
	if err != nil {
		b.Fatalf("New error = %v", err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := bastion.Execute(ctx, breaker, benchOp); err != nil {
			b.Fatalf("Execute error = %v", err)
		}
	}
}

// The clock is frozen and never advanced: OpenTimeout is set far beyond
// anything the benchmark could take in wall time, so effectiveState never
// evaluates the lazy Open -> Half-Open transition mid-run. Every iteration
// measures exactly the rejection path in admit, nothing else.
func BenchmarkExecute_OpenRejectionPath(b *testing.B) {
	clock := newFakeClock()
	breaker, err := bastion.New("bench",
		bastion.WithFailureThreshold(1),
		bastion.WithOpenTimeout(24*time.Hour),
		bastion.WithClock(clock),
	)
	if err != nil {
		b.Fatalf("New error = %v", err)
	}
	if _, err := bastion.Execute(context.Background(), breaker, func(context.Context) (int, error) {
		return 0, errBoom
	}); err == nil {
		b.Fatal("setup: expected the call to fail and open the circuit")
	}
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := bastion.Execute(ctx, breaker, benchOp); err == nil {
			b.Fatal("Execute error = nil, want ErrOpenState (rejection path is no longer being measured)")
		}
	}
}

// The baseline this package's overhead is measured against: the same
// operation, no breaker at all.
func BenchmarkOpDirect(b *testing.B) {
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := benchOp(ctx); err != nil {
			b.Fatalf("benchOp error = %v", err)
		}
	}
}

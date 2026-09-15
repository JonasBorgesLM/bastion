package bastion_test

import (
	"context"
	"testing"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// NFR-02: the overhead is measured, not asserted. Five shapes, so a reader
// can see what a breaker costs relative to no breaker at all, on both the
// path that does real work and the path that does none, serial and under
// contention:
//
//   - BenchmarkExecute_ClosedHappyPath: the cost Execute adds on top of the
//     baseline when every call is admitted and succeeds, one goroutine.
//   - BenchmarkExecute_OpenRejectionPath: the cost of the fast-fail path,
//     which never invokes the operation at all, one goroutine.
//   - BenchmarkExecute_ClosedHappyPathParallel,
//     BenchmarkExecute_OpenRejectionPathParallel: the same two paths under
//     b.RunParallel, run with -cpu 1,2,4,8 to see the same mutex's
//     throughput ceiling under real contention (ADR-0016, issue #55) --
//     these use a near-zero-cost op, deliberately adversarial (the mutex is
//     never held while a real op runs), so treat the absolute numbers as a
//     worst case, not a representative one.
//   - BenchmarkOpDirect: the baseline -- the same operation, called with no
//     breaker in front of it.
//
// Run with `go test -bench=. -benchmem -cpu 1,2,4,8 ./...` and record ns/op
// and allocs/op somewhere a later change to admit/complete can be compared
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

// The same happy path as BenchmarkExecute_ClosedHappyPath, run by every
// goroutine b.RunParallel starts against one shared Breaker -- the shape
// that actually exercises b.mu's contention, which a single-goroutine
// benchmark cannot (ADR-0016, issue #55).
func BenchmarkExecute_ClosedHappyPathParallel(b *testing.B) {
	breaker, err := bastion.New("bench")
	if err != nil {
		b.Fatalf("New error = %v", err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := bastion.Execute(ctx, breaker, benchOp); err != nil {
				b.Fatalf("Execute error = %v", err)
			}
		}
	})
}

// The same rejection path as BenchmarkExecute_OpenRejectionPath, under
// b.RunParallel (ADR-0016, issue #55). Same frozen-clock setup: OpenTimeout
// is far beyond anything the benchmark could take, so every goroutine's
// every call measures admit's rejection branch alone.
func BenchmarkExecute_OpenRejectionPathParallel(b *testing.B) {
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
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := bastion.Execute(ctx, breaker, benchOp); err == nil {
				b.Fatal("Execute error = nil, want ErrOpenState (rejection path is no longer being measured)")
			}
		}
	})
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

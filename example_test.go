package bastion_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JonasBorgesLM/bastion"
)

// Example shows the whole shape: construct a breaker, guard a call, and
// observe the state change once the dependency has failed enough times to
// trip it. A rejected call never reaches the dependency at all.
func Example() {
	clock := newFakeClock()
	var transitions []string
	breaker, err := bastion.New("payments-api",
		bastion.WithFailureThreshold(2),
		bastion.WithClock(clock),
		bastion.WithHooks(bastion.Hooks{
			OnStateChange: func(_ context.Context, ev bastion.StateChangeEvent) {
				transitions = append(transitions, fmt.Sprintf("%s: %s -> %s", ev.Name, ev.From, ev.To))
			},
		}),
	)
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	callDependency := func(context.Context) (string, error) {
		return "", errors.New("connection refused")
	}

	// Two failures reach the threshold and open the circuit.
	_, _ = bastion.Execute(ctx, breaker, callDependency)
	_, _ = bastion.Execute(ctx, breaker, callDependency)

	// A third call is rejected without the dependency ever being called.
	_, err = bastion.Execute(ctx, breaker, callDependency)
	fmt.Println("rejected:", errors.Is(err, bastion.ErrOpenState))
	fmt.Println("state:", breaker.State())
	fmt.Println("transitions:", transitions)

	// Output:
	// rejected: true
	// state: open
	// transitions: [payments-api: closed -> open]
}

// ExampleRetry shows the composition [ADR-0006] recommends: Retry wraps
// Execute, so every attempt is its own, individually admitted and counted
// call. A transient failure retries; the breaker never sees enough of them
// in this example to trip.
func ExampleRetry() {
	breaker, err := bastion.New("flaky-api")
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	attempts := 0
	unreliableCall := func(context.Context) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("timeout")
		}
		return "ok", nil
	}

	policy := bastion.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}
	result, err := bastion.Retry(ctx, policy, func(ctx context.Context) (string, error) {
		return bastion.Execute(ctx, breaker, unreliableCall)
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("result:", result)
	fmt.Println("attempts:", attempts)

	// Output:
	// result: ok
	// attempts: 3
}

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

// response stands in for whatever a dependency answers with. An HTTP response
// is the usual case; bastion itself never imports net/http (NFR-03).
type response struct{ status int }

// unhealthyError carries a result the caller still wants out of Execute, as
// the error Execute counts against the circuit (ADR-0021).
type unhealthyError struct{ resp response }

func (e *unhealthyError) Error() string { return fmt.Sprintf("dependency answered %d", e.resp.status) }

// ExampleExecute_resultAsFailure shows how to count a bad *result* as a
// failure. A classifier (WithIsFailure) only ever sees the error the operation
// returns, and an answer like an HTTP 503 is not one, so it would count as a
// success on every call. The operation turns the result it considers unhealthy
// into an error carrying it; the caller unwraps it after Execute, so it still
// gets the dependency's own answer while the circuit is closed.
func ExampleExecute_resultAsFailure() {
	breaker, err := bastion.New("inventory", bastion.WithFailureThreshold(2))
	if err != nil {
		panic(err)
	}
	dependency := func(context.Context) (response, error) { return response{status: 503}, nil }

	guarded := func(ctx context.Context) (response, error) {
		resp, err := bastion.Execute(ctx, breaker, func(ctx context.Context) (response, error) {
			resp, err := dependency(ctx)
			if err == nil && resp.status >= 500 {
				return response{}, &unhealthyError{resp: resp}
			}
			return resp, err
		})
		var unhealthy *unhealthyError
		if errors.As(err, &unhealthy) {
			return unhealthy.resp, nil
		}
		return resp, err
	}

	for i := 1; i <= 3; i++ {
		resp, err := guarded(context.Background())
		fmt.Printf("call %d: status=%d err=%v\n", i, resp.status, err)
	}

	// Output:
	// call 1: status=503 err=<nil>
	// call 2: status=503 err=<nil>
	// call 3: status=0 err=bastion: circuit is open
}

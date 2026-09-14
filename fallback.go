package bastion

import "context"

// Fallback runs fb and returns its result when err is non-nil, or passes
// result and err through unchanged when err is nil (FR-08).
//
// Fallback is called after [Execute] has already returned, never nested
// inside the operation Execute wraps:
//
//	result, err := bastion.Execute(ctx, breaker, realOp)
//	result, err = bastion.Fallback(ctx, result, err, myFallback)
//
// This is the only correct place for it (docs/adr/0008-fallback-is-a-post-execute-call-site-function.md):
// a fallback nested inside op would let its own success register as a
// success against the breaker, silently telling it the dependency answered
// when the fallback did. Called here instead, Fallback never holds a
// reference to a [Breaker] and cannot affect its counters either way — a
// property the test suite verifies against the breaker's observable state,
// not merely by the absence of a code path.
//
// Fallback does not distinguish a rejection ([ErrOpenState],
// [ErrTooManyRequests]) from a genuine operation failure, including one
// surfaced by an exhausted [Retry] loop: err is any of these, uniformly, and
// fb runs on all of them. If a caller wants a fallback only for one specific
// case, that is one errors.Is check before calling Fallback, not a job
// Fallback takes on itself.
//
// If ctx is already Done, Fallback returns result and err unchanged without
// calling fb at all — a caller who has already cancelled has walked away,
// and running fb on their behalf would be unrequested work, the same
// reasoning [Execute] and [Retry] both apply to ctx cancellation elsewhere.
func Fallback[T any](ctx context.Context, result T, err error, fb func(context.Context, error) (T, error)) (T, error) {
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	return fb(ctx, err)
}

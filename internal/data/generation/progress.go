package generation

import "context"

type progressKey struct{}

// WithProgress returns a context whose Replicate calls report how many of
// a generation's chunks are embedded and stored so far. Carried on the
// context because a sync's caller is several layers above Replicate, and
// progress is observation, not an input to the work.
func WithProgress(ctx context.Context, fn func(done, total int)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func reportProgress(ctx context.Context, done, total int) {
	if fn, ok := ctx.Value(progressKey{}).(func(done, total int)); ok {
		fn(done, total)
	}
}

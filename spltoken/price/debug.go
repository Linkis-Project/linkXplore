package price

import "context"

type debugKey struct{}

type Debugf func(format string, args ...any)

// WithPriceDebug attaches a logger (e.g. t.Logf) to the context.
func WithPriceDebug(ctx context.Context, f Debugf) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, debugKey{}, f)
}

// dbg prints a debug line if a Debugf logger is in the context.
func dbg(ctx context.Context, format string, args ...any) {
	if v := ctx.Value(debugKey{}); v != nil {
		if f, ok := v.(Debugf); ok && f != nil {
			f(format, args...)
		}
	}
}

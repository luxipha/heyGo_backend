package scheduling

import "context"

type dispatchKey struct{}

// DispatchContext is used only by the durable backend scheduler. HTTP booking
// requests cannot activate a fare reserved for future dispatch.
func DispatchContext(ctx context.Context, booking string) context.Context {
	return context.WithValue(ctx, dispatchKey{}, booking)
}
func DispatchBooking(ctx context.Context) string { s, _ := ctx.Value(dispatchKey{}).(string); return s }

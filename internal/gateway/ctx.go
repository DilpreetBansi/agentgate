package gateway

import "context"

func contextWith(ctx context.Context, c *call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

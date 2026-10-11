package executor

import "context"

type executionModelContextKey struct{}
type executionModelCallbackKey struct{}

// WithPinnedExecutionModel fixes the upstream model for durable task retrieval.
func WithPinnedExecutionModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, executionModelContextKey{}, model)
}

// WithExecutionModelCallback observes the actual model passed to an executor.
func WithExecutionModelCallback(ctx context.Context, callback func(string)) context.Context {
	return context.WithValue(ctx, executionModelCallbackKey{}, callback)
}

// PrepareExecutionModel applies an exact pin and publishes the selected model.
func PrepareExecutionModel(ctx context.Context, model string) string {
	if ctx == nil {
		return model
	}
	if pinned, _ := ctx.Value(executionModelContextKey{}).(string); pinned != "" {
		model = pinned
	}
	if callback, _ := ctx.Value(executionModelCallbackKey{}).(func(string)); callback != nil {
		callback(model)
	}
	return model
}

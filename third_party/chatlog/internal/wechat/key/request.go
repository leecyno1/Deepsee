package key

import "context"

// Request carries optional extraction behavior without stringly-typed context
// keys. Context remains responsible only for cancellation and request-scoped
// metadata.
type Request struct {
	Status       func(string)
	ImageOnly    bool
	DataOnly     bool
	ForceRescan  bool
	ForceRefresh bool
}

type requestContextKey struct{}

func WithRequest(ctx context.Context, request Request) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestContextKey{}, request)
}

func RequestFromContext(ctx context.Context) Request {
	if ctx == nil {
		return Request{}
	}
	request, _ := ctx.Value(requestContextKey{}).(Request)
	return request
}

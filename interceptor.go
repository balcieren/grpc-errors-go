package grpcerrors

import (
	"context"

	"google.golang.org/grpc"
)

// ErrorInterceptorFunc is a callback invoked when a gRPC handler returns
// an error that has a registered domain error code (in google.rpc.ErrorInfo).
// It receives the context, the original error, and the resolved Error definition.
//
// Common use cases: logging, metrics, tracing, error transformation.
type ErrorInterceptorFunc func(ctx context.Context, err error, def Error)

// ErrorInterceptor is a server-side gRPC unary interceptor that hooks into
// error responses. When a handler returns an error with a registered domain
// error code (via google.rpc.ErrorInfo), the interceptor resolves it from the
// Registry and invokes the callback.
//
// Example:
//
//	interceptor := gerr.ErrorInterceptor(func(ctx context.Context, err error, def gerr.Error) {
//	    slog.ErrorContext(ctx, "rpc error",
//	        "code", def.ErrorCode,
//	        "status_code", def.StatusCode,
//	        "retryable", def.Retryable,
//	    )
//	    metrics.IncrCounter("rpc.error", "code", def.ErrorCode)
//	})
//
//	s := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
func ErrorInterceptor(fn ErrorInterceptorFunc) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			if def, found := FromError(err); found {
				fn(ctx, err, def)
			}
		}
		return resp, err
	}
}

// StreamingErrorInterceptor is the streaming counterpart of ErrorInterceptor.
// It hooks into streaming RPC error responses and invokes the callback when
// a handler returns an error with a registered domain error code.
//
// Example:
//
//	interceptor := gerr.StreamingErrorInterceptor(func(ctx context.Context, err error, def gerr.Error) {
//	    slog.ErrorContext(ctx, "streaming rpc error", "code", def.ErrorCode)
//	})
//
//	s := grpc.NewServer(grpc.StreamInterceptor(interceptor))
func StreamingErrorInterceptor(fn ErrorInterceptorFunc) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, ss)
		if err != nil {
			if def, found := FromError(err); found {
				fn(ss.Context(), err, def)
			}
		}
		return err
	}
}
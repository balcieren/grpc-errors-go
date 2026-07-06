package grpcerrors

import (
	"context"
	"sync/atomic"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/protoadapt"
)

// contextExtractorFn is a configurable function that extracts metadata from a context.
// The returned map is merged into the error's template data and ErrorInfo metadata.
var contextExtractorFn atomic.Value

func init() {
	contextExtractorFn.Store(func(context.Context) M { return nil })
}

// SetContextExtractor configures a function that extracts template data from a context.
// The extracted data is merged with the data passed to NewCtx.
// This is useful for automatically injecting trace IDs, request IDs, etc.
//
// Example:
//
//	gerr.SetContextExtractor(func(ctx context.Context) gerr.M {
//	    if traceID, ok := ctx.Value("trace_id").(string); ok {
//	        return gerr.M{"trace_id": traceID}
//	    }
//	    return nil
//	})
func SetContextExtractor(fn func(ctx context.Context) M) {
	if fn != nil {
		contextExtractorFn.Store(fn)
	}
}

// NewCtx creates a gRPC error from a registered error code, merging template data
// from the context (via SetContextExtractor) with the provided data.
// Context-extracted data is overridden by explicitly provided data.
//
// Example:
//
//	return gerr.NewCtx(ctx, gerr.ErrNotFound, nil)
func NewCtx(ctx context.Context, code ErrorCoder, data M) error {
	v := contextExtractorFn.Load()
	if v == nil {
		return New(code, data)
	}
	extractor, ok := v.(func(context.Context) M)
	if !ok {
		return New(code, data)
	}
	ctxData := extractor(ctx)

	merged := make(M, len(ctxData)+len(data))
	for k, v := range ctxData {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}

	return New(code, merged)
}

// ErrorBuilder provides a chainable API for constructing gRPC errors with
// additional details, retry delays, domain overrides, and field violations.
//
// Example:
//
//	err := gerr.NewBuilder(gerr.ErrInvalidArgument, nil).
//	    WithFieldViolation("email", "already registered").
//	    WithRetryDelay(5 * time.Second).
//	    Build()
type ErrorBuilder struct {
	code       ErrorCoder
	data       M
	retryDelay time.Duration
	details    []protoadapt.MessageV1
	violations []fieldViolation
	domain     string
	customMsg  string
}

type fieldViolation struct {
	field       string
	description string
}

// NewBuilder creates a new ErrorBuilder for the given error code and template data.
//
// Example:
//
//	b := gerr.NewBuilder(gerr.ErrNotFound, nil)
func NewBuilder(code ErrorCoder, data M) *ErrorBuilder {
	return &ErrorBuilder{
		code: code,
		data: data,
	}
}

// WithDetail adds a protobuf detail to the error.
func (b *ErrorBuilder) WithDetail(d protoadapt.MessageV1) *ErrorBuilder {
	if d != nil {
		b.details = append(b.details, d)
	}
	return b
}

// WithRetryDelay sets a custom retry delay for the error's RetryInfo.
func (b *ErrorBuilder) WithRetryDelay(d time.Duration) *ErrorBuilder {
	b.retryDelay = d
	return b
}

// WithDomain overrides the error domain for this error only.
// This does not affect the global domain configuration.
func (b *ErrorBuilder) WithDomain(domain string) *ErrorBuilder {
	b.domain = domain
	return b
}

// WithFieldViolation adds a google.rpc.BadRequest FieldViolation to the error.
func (b *ErrorBuilder) WithFieldViolation(field, description string) *ErrorBuilder {
	b.violations = append(b.violations, fieldViolation{field, description})
	return b
}

// WithMessage overrides the message template for this error.
func (b *ErrorBuilder) WithMessage(msg string) *ErrorBuilder {
	b.customMsg = msg
	return b
}

// Build constructs the final gRPC error with all configured options.
func (b *ErrorBuilder) Build() error {
	// Build a BadRequest detail (if any field violations were added)
	var extraBad *errdetails.BadRequest
	if len(b.violations) > 0 {
		violations := make([]*errdetails.BadRequest_FieldViolation, 0, len(b.violations))
		for _, v := range b.violations {
			violations = append(violations, &errdetails.BadRequest_FieldViolation{
				Field:       v.field,
				Description: v.description,
			})
		}
		extraBad = &errdetails.BadRequest{FieldViolations: violations}
	}

	err := createError(b.code, b.data, b.retryDelay, b.customMsg, b.domain, extraBad)

	// Add any extra details
	if len(b.details) > 0 {
		_ = WithDetails(err, b.details...)
	}

	return err
}
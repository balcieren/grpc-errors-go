package grpcerrors

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ErrorCoder is the interface accepted by all error creation functions.
// Both ErrorCode (named string) and *CodedError satisfy this interface.
type ErrorCoder interface {
	Code() string
}

// ErrorCode is a type-safe error code identifier.
// Use this instead of raw strings for compile-time safety.
// ErrorCode implements the error interface, enabling errors.Is matching:
//
//	const MyError gerr.ErrorCode = "ERROR_CUSTOM"
//	gerr.New(MyError, gerr.M{"key": "val"})
//	errors.Is(err, MyError) // true if err was created with MyError
type ErrorCode string

// Code returns the string representation of the error code.
// This method satisfies the ErrorCoder interface.
func (c ErrorCode) Code() string { return string(c) }

// Error implements the error interface, allowing ErrorCode to be used
// directly as an error sentinel for errors.Is comparisons.
func (c ErrorCode) Error() string { return string(c) }

// M is a shorthand type for template data maps.
// Keys are placeholder names, values are their replacements.
//
// Example:
//
//	grpcerrors.M{"id": "123", "email": "user@example.com"}
type M map[string]string

// ErrorLogger is called for every error creation (New, NewWithMessage, Wrap, etc.).
// Default: no-op (no logging).
//
// Example:
//
//	gerr.SetErrorLogger(func(code string, statusCode codes.Code, retryable bool, data gerr.M) {
//	    slog.Info("error created", "code", code, "status_code", statusCode)
//	})
type ErrorLogger func(code string, statusCode codes.Code, retryable bool, data M)

// ValidationLogger is called when template validation fails.
// Default: no-op (no logging, no panic).
//
// Example:
//
//	gerr.SetValidationLogger(func(code string, data gerr.M, err error) {
//	    slog.Error("template validation failed", "code", code, "error", err)
//	})
type ValidationLogger func(code string, data M, err error)

// errorLoggerVal stores the current error logger atomically. Default: no-op.
var errorLoggerVal atomic.Value

// validationLoggerVal stores the current validation logger atomically. Default: no-op.
var validationLoggerVal atomic.Value

// getErrorLogger returns the current error logger.
func getErrorLogger() ErrorLogger {
	return errorLoggerVal.Load().(ErrorLogger)
}

// getValidationLogger returns the current validation logger.
func getValidationLogger() ValidationLogger {
	return validationLoggerVal.Load().(ValidationLogger)
}

// SetErrorLogger configures a custom logger for all error creations.
// This is safe for concurrent use.
//
// Example:
//
//	// Zap integration
//	logger, _ := zap.NewProduction()
//	gerr.SetErrorLogger(func(code string, statusCode codes.Code, retryable bool, data gerr.M) {
//	    logger.Info("error created",
//	        zap.String("code", code),
//	        zap.String("status_code", statusCode.String()),
//	        zap.Bool("retryable", retryable),
//	    )
//	})
func SetErrorLogger(fn ErrorLogger) {
	if fn == nil {
		fn = func(string, codes.Code, bool, M) {}
	}
	errorLoggerVal.Store(fn)
}

// SetValidationLogger configures a custom logger for template validation failures.
// This is safe for concurrent use.
func SetValidationLogger(fn ValidationLogger) {
	if fn == nil {
		fn = func(string, M, error) {}
	}
	validationLoggerVal.Store(fn)
}

// domainVal stores the configured error domain atomically.
var domainVal atomic.Value

// TrailerKey is the gRPC trailer metadata key used by default to surface the
// domain error code and retryable flag on the wire. Clients can read these
// trailers for fast inspection without parsing protobuf details.
const (
	TrailerKeyCode      = "x-error-code"
	TrailerKeyRetryable = "x-retryable"
)

func init() {
	domainVal.Store("grpcerrors")
	errorLoggerVal.Store(ErrorLogger(func(code string, statusCode codes.Code, retryable bool, data M) {}))
	validationLoggerVal.Store(ValidationLogger(func(code string, data M, err error) {}))
}

// GetDomain returns the configured error domain used in ErrorInfo details.
func GetDomain() string {
	return domainVal.Load().(string)
}

// SetDomain configures the error domain used in google.rpc.ErrorInfo details.
// Default is "grpcerrors". This is safe for concurrent use.
//
// Example:
//
//	gerr.SetDomain("myapp")
func SetDomain(domain string) {
	if domain != "" {
		domainVal.Store(domain)
	}
}

// grpcError wraps a *status.Status along with the domain error code and the
// trailers that should be sent alongside the error. It implements the error
// interface and is the canonical error type returned by this package.
//
// Handlers should return it as-is from their method handlers. gRPC will
// transparently unwrap it to a *status.Status on the wire while
// SetTrailer propagates the custom metadata to the client.
type grpcError struct {
	st       *status.Status
	code     string // domain error code (e.g. "ERROR_USER_NOT_FOUND")
	retryable bool
	coded    *CodedError // wrapped error carrying code+msg for errors.Is/As
}

// Error implements the error interface.
func (e *grpcError) Error() string {
	if e == nil || e.st == nil {
		return ""
	}
	return e.st.Message()
}

// GRPCStatus implements the interface checked by status.FromError and
// google.golang.org/grpc/status.Convert. Returning a *status.Status here
// makes the error indistinguishable from a normal gRPC error on the wire
// while still allowing our metadata (code, retryable) to ride along.
func (e *grpcError) GRPCStatus() *status.Status {
	if e == nil || e.st == nil {
		return status.New(codes.Unknown, "")
	}
	return e.st
}

// Is reports whether the target is an ErrorCode or *CodedError matching this
// error's code. This enables errors.Is(err, gerr.ErrNotFound) to return true
// for errors created via gerr.New(gerr.ErrNotFound, ...).
func (e *grpcError) Is(target error) bool {
	if e == nil || e.coded == nil {
		return false
	}
	return e.coded.Is(target)
}

// As allows errors.As(err, &coded) to retrieve the *CodedError carrying the
// domain error code.
func (e *grpcError) As(target any) bool {
	if e == nil || e.coded == nil {
		return false
	}
	if t, ok := target.(**CodedError); ok {
		*t = e.coded
		return true
	}
	return false
}

// Unwrap returns the underlying *CodedError for errors.Is / errors.As chains.
func (e *grpcError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.coded
}

// setTrailer attaches the error-code and retryable trailers to the gRPC
// stream so that clients can read them without parsing the protobuf
// ErrorInfo detail. It is a no-op when the stream is not a gRPC server
// transport stream (e.g. when the error is created client-side).
func setTrailer(ctx context.Context, code string, retryable bool) {
	if ctx == nil {
		return
	}
	st := grpc.ServerTransportStreamFromContext(ctx)
	if st == nil {
		return
	}
	_ = st.SetTrailer(metadata.Pairs(
		TrailerKeyCode, code,
		TrailerKeyRetryable, boolToString(retryable),
	))
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// attachDetails attaches google.rpc.ErrorInfo and (optionally) google.rpc.RetryInfo
// protobuf details to a *status.Status and returns a new *status.Status.
// If domainOverride is non-empty, it is used instead of the global domain.
func attachDetails(st *status.Status, code string, data M, retryable bool, retryDelay time.Duration, domainOverride string, extraBad *errdetails.BadRequest) *status.Status {
	domain := domainOverride
	if domain == "" {
		domain = GetDomain()
	}

	info := &errdetails.ErrorInfo{
		Reason: code,
		Domain: domain,
	}
	if len(data) > 0 {
		info.Metadata = make(map[string]string, len(data))
		for k, v := range data {
			info.Metadata[k] = v
		}
	}

	var updated *status.Status
	var err error
	updated, err = st.WithDetails(info)
	if err != nil {
		return st
	}

	if retryable {
		retryInfo := &errdetails.RetryInfo{
			RetryDelay: durationpb.New(retryDelay),
		}
		if u, e := updated.WithDetails(retryInfo); e == nil {
			updated = u
		}
	}

	if extraBad != nil {
		if u, e := updated.WithDetails(extraBad); e == nil {
			updated = u
		}
	}

	return updated
}

// createError is the internal workhorse that all error creation functions delegate to.
// It accepts optional retryDelay (if retryDelay > 0), optional customMsg (if non-empty),
// optional domain override (if non-empty), and an optional BadRequest detail.
func createError(code ErrorCoder, data M, retryDelay time.Duration, customMsg string, domainOverride string, extraBad *errdetails.BadRequest) error {
	codeStr := extractCode(code)
	e, ok := Lookup(ErrorCode(codeStr))
	if !ok {
		st := status.New(codes.Internal, fmt.Sprintf("unknown error code: %s", codeStr))
		st = attachDetails(st, codeStr, data, false, 0, domainOverride, extraBad)
		getErrorLogger()(codeStr, codes.Internal, false, data)
		return &grpcError{st: st, code: codeStr, coded: &CodedError{code: codeStr, msg: fmt.Sprintf("unknown error code: %s", codeStr)}}
	}

	tpl := e.MessageTpl
	if customMsg != "" {
		tpl = customMsg
	}

	if verr := ValidateTemplate(tpl, data); verr != nil {
		getValidationLogger()(codeStr, data, verr)
	}

	msg := FormatTemplate(tpl, data)
	st := status.New(e.StatusCode, msg)

	effectiveDelay := e.RetryDelay
	if retryDelay > 0 {
		effectiveDelay = retryDelay
	}
	st = attachDetails(st, codeStr, data, e.Retryable, effectiveDelay, domainOverride, extraBad)
	getErrorLogger()(codeStr, e.StatusCode, e.Retryable, data)

	return &grpcError{
		st:        st,
		code:      codeStr,
		retryable: e.Retryable,
		coded:      &CodedError{code: codeStr, msg: msg},
	}
}

// New creates a gRPC error from a registered error code and template data.
// It looks up the error definition in the Registry, formats the message template
// with the provided data, and returns an error carrying the appropriate gRPC
// status code plus google.rpc.ErrorInfo (and RetryInfo if retryable) details.
//
// The code parameter must implement ErrorCoder (e.g. ErrorCode or *CodedError).
// If the error code is not found in the Registry, it falls back to codes.Internal.
//
// Example:
//
//	// Using ErrorCode constant
//	return grpcerrors.New(grpcerrors.ErrNotFound, nil)
//
//	// Using generated error sentinel
//	return grpcerrors.New(userv1.ErrUserNotFound, grpcerrors.M{"id": "123"})
func New(code ErrorCoder, data M) error {
	return createError(code, data, 0, "", "", nil)
}

// NewWithRetry creates a gRPC error from a registered error code with a custom retry delay.
// This overrides the retry delay defined in the Registry (if any).
//
// Example:
//
//	return gerr.NewWithRetry(gerr.ErrUnavailable, gerr.M{}, 5*time.Second)
func NewWithRetry(code ErrorCoder, data M, retryDelay time.Duration) error {
	return createError(code, data, retryDelay, "", "", nil)
}

// extractCode extracts the error code string from an ErrorCoder implementation.
func extractCode(code ErrorCoder) string {
	if code == nil {
		return ""
	}
	return code.Code()
}

// NewWithMessage creates a gRPC error using a custom message template instead of
// the one defined in the Registry. The error code is still used to determine
// the gRPC status code and retryable flag.
//
// Example:
//
//	return grpcerrors.NewWithMessage(
//	    grpcerrors.ErrNotFound,
//	    "User '{{id}}' does not exist in tenant '{{tenant}}'",
//	    grpcerrors.M{"id": "123", "tenant": "acme"},
//	)
func NewWithMessage(code ErrorCoder, customMsg string, data M) error {
	return createError(code, data, 0, customMsg, "", nil)
}

// FromCode creates a gRPC error directly from a gRPC status code and message.
// This bypasses the Registry entirely and is useful for one-off errors that don't
// need template support.
//
// Example:
//
//	return grpcerrors.FromCode(codes.Internal, "unexpected database error")
func FromCode(code codes.Code, msg string) error {
	return status.Error(code, msg)
}

// Wrap creates a gRPC error that wraps an underlying error with context from
// the Registry. The original error message is preserved and the template message
// is prepended. This is useful for adding user-facing context to internal errors.
//
// Example:
//
//	user, err := db.GetUser(ctx, id)
//	if err != nil {
//	    return grpcerrors.Wrap(grpcerrors.ErrNotFound, err, nil)
//	}
func Wrap(code ErrorCoder, err error, data M) error {
	codeStr := extractCode(code)
	e, ok := Lookup(ErrorCode(codeStr))
	if !ok {
		wrapped := fmt.Errorf("unknown error code %s: %w", codeStr, err)
		st := status.New(codes.Internal, wrapped.Error())
		st = attachDetails(st, codeStr, data, false, 0, "", nil)
		getErrorLogger()(codeStr, codes.Internal, false, data)
		return &grpcError{st: st, code: codeStr, coded: &CodedError{code: codeStr, msg: wrapped.Error()}}
	}

	if verr := ValidateTemplate(e.MessageTpl, data); verr != nil {
		getValidationLogger()(codeStr, data, verr)
	}

	msg := FormatTemplate(e.MessageTpl, data)
	wrapped := fmt.Errorf("%w: %w", &CodedError{code: codeStr, msg: msg}, err)
	st := status.New(e.StatusCode, wrapped.Error())
	st = attachDetails(st, codeStr, data, e.Retryable, e.RetryDelay, "", nil)
	getErrorLogger()(codeStr, e.StatusCode, e.Retryable, data)

	return &grpcError{st: st, code: codeStr, retryable: e.Retryable, coded: &CodedError{code: codeStr, msg: wrapped.Error()}}
}

// IsRetryableCode checks whether an error code is marked as retryable in the Registry.
// Returns false if the error code is not found.
//
// Example:
//
//	grpcerrors.IsRetryableCode(grpcerrors.ErrUnavailable) // true
//	grpcerrors.IsRetryableCode(grpcerrors.ErrNotFound)    // false
func IsRetryableCode(code ErrorCode) bool {
	e, ok := Lookup(code)
	return ok && e.Retryable
}

// IsRetryableErr checks whether an error carries retryable metadata.
// It first checks for the x-retryable gRPC trailer, then falls back to the
// presence of a google.rpc.RetryInfo detail.
// Returns false for non-gRPC errors or errors without retryable metadata.
//
// Example:
//
//	err := grpcerrors.New(grpcerrors.ErrUnavailable, nil)
//	grpcerrors.IsRetryableErr(err) // true
func IsRetryableErr(err error) bool {
	if ge, ok := err.(*grpcError); ok && ge != nil {
		return ge.retryable
	}
	if _, ok := ExtractRetryInfo(err); ok {
		return true
	}
	return false
}

// IsRetryable checks whether an error code or error is marked as retryable.
//
// Deprecated: Use IsRetryableCode for ErrorCode values or IsRetryableErr for error values.
func IsRetryable(codeOrErr any) bool {
	switch v := codeOrErr.(type) {
	case ErrorCode:
		return IsRetryableCode(v)
	case error:
		return IsRetryableErr(v)
	}
	return false
}

// StatusCode returns the gRPC status code for a registered error code.
// Returns codes.Internal if the error code is not found.
func StatusCode(code ErrorCode) codes.Code {
	e, ok := Lookup(code)
	if !ok {
		return codes.Internal
	}
	return e.StatusCode
}

// Newf creates a gRPC error from a registered error code with a formatted message.
// Instead of using template placeholders, this uses fmt.Sprintf-style formatting.
// The error code is still used to determine the gRPC status code and retryable flag.
//
// Example:
//
//	return gerr.Newf(gerr.ErrNotFound, "User %q not found in org %s", userID, orgName)
func Newf(code ErrorCoder, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return createError(code, nil, 0, msg, "", nil)
}

// CodedError is an error type that carries a domain error code alongside
// the standard error interface. It enables errors.As support for matching
// errors by their registered code.
//
// Example:
//
//	err := cerr.New(cerr.ErrNotFound, nil)
//	var coded *cerr.CodedError
//	if errors.As(err.Unwrap(), &coded) {
//	    fmt.Println(coded.Code()) // "ERROR_NOT_FOUND"
//	}
//
// CodedError also implements Is() so that errors.Is works with ErrorCode sentinels:
//
//	errors.Is(err, cerr.ErrNotFound) // true
type CodedError struct {
	code string
	msg  string
}

// Error implements the error interface.
func (e *CodedError) Error() string { return e.msg }

// Code returns the domain error code string.
// This method satisfies the ErrorCoder interface.
func (e *CodedError) Code() string {
	if e == nil {
		return ""
	}
	return e.code
}

// Is reports whether the target is an ErrorCode or *CodedError matching this error's code.
// This enables errors.Is(err, cerr.ErrNotFound) to return true for errors created
// via cerr.New(cerr.ErrNotFound, ...).
func (e *CodedError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch t := target.(type) {
	case ErrorCode:
		return string(t) == e.code
	case *CodedError:
		return t != nil && t.code == e.code
	}
	return false
}

// ErrorCode returns the domain error code (e.g. "ERROR_NOT_FOUND").
// Deprecated: Use Code() instead.
func (e *CodedError) ErrorCode() string { return e.Code() }

// WithDetails adds structured protobuf details to an existing gRPC error.
// Details are protobuf messages that can carry domain-specific error information.
// Returns the same error for method chaining.
//
// Example:
//
//	err := gerr.New(gerr.ErrInvalidArgument, nil)
//	gerr.WithDetails(err, detail)
func WithDetails(err error, details ...protoadapt.MessageV1) error {
	ge, ok := err.(*grpcError)
	if !ok || ge == nil {
		return err
	}
	for _, d := range details {
		if d == nil {
			continue
		}
		if st, derr := ge.st.WithDetails(d); derr == nil {
			ge.st = st
		}
	}
	return err
}

// WithFieldViolation adds a google.rpc.BadRequest with a FieldViolation to a gRPC error.
// This is useful for communicating input validation failures to clients.
// Returns the same error for method chaining.
//
// Example:
//
//	err := gerr.New(gerr.ErrInvalidArgument, nil)
//	gerr.WithFieldViolation(err, "email", "must be a valid email address")
func WithFieldViolation(err error, field, description string) error {
	violation := &errdetails.BadRequest_FieldViolation{
		Field:       field,
		Description: description,
	}
	badRequest := &errdetails.BadRequest{
		FieldViolations: []*errdetails.BadRequest_FieldViolation{violation},
	}
	return WithDetails(err, badRequest)
}

// FromError extracts the domain error definition from a gRPC error.
// It reads the google.rpc.ErrorInfo detail to look up the corresponding
// Error definition in the Registry. Falls back to the x-error-code trailer
// if available on the context.
func FromError(err error) (Error, bool) {
	if err == nil {
		return Error{}, false
	}

	if ge, ok := err.(*grpcError); ok && ge != nil {
		return Lookup(ErrorCode(ge.code))
	}

	if info, ok := ExtractErrorInfo(err); ok {
		return Lookup(ErrorCode(info.Reason))
	}
	return Error{}, false
}

// ExtractErrorCode extracts the domain error code string from a gRPC error.
// It first checks the grpcError wrapper, then the google.rpc.ErrorInfo detail,
// and finally falls back to gRPC trailing metadata (if available via context).
func ExtractErrorCode(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	if ge, ok := err.(*grpcError); ok && ge != nil {
		return ge.code, true
	}

	if info, ok := ExtractErrorInfo(err); ok {
		return info.Reason, true
	}
	return "", false
}

// ExtractErrorInfo extracts a google.rpc.ErrorInfo detail from a gRPC error, if present.
func ExtractErrorInfo(err error) (*errdetails.ErrorInfo, bool) {
	if err == nil {
		return nil, false
	}
	// Fast path: our own grpcError wraps details already.
	if ge, ok := err.(*grpcError); ok && ge != nil {
		for _, d := range ge.st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok {
				return info, true
			}
		}
	}

	st, ok := status.FromError(err)
	if !ok {
		// Try errors.As for wrapped errors.
		var ge *grpcError
		if errors.As(err, &ge) && ge != nil {
			st = ge.st
		} else {
			return nil, false
		}
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info, true
		}
	}
	return nil, false
}

// ExtractRetryInfo extracts a google.rpc.RetryInfo detail from a gRPC error, if present.
func ExtractRetryInfo(err error) (*errdetails.RetryInfo, bool) {
	if err == nil {
		return nil, false
	}
	if ge, ok := err.(*grpcError); ok && ge != nil {
		for _, d := range ge.st.Details() {
			if info, ok := d.(*errdetails.RetryInfo); ok {
				return info, true
			}
		}
	}

	st, ok := status.FromError(err)
	if !ok {
		var ge *grpcError
		if errors.As(err, &ge) && ge != nil {
			st = ge.st
		} else {
			return nil, false
		}
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.RetryInfo); ok {
			return info, true
		}
	}
	return nil, false
}

// MatchesError reports whether err is a gRPC error matching the given error code.
// It checks the embedded grpcError code and the google.rpc.ErrorInfo detail,
// making it resilient to transports that strip metadata.
//
// Example:
//
//	if grpcerrors.MatchesError(err, grpcerrors.ErrNotFound) {
//	    fmt.Println("not found")
//	}
func MatchesError(err error, code ErrorCode) bool {
	extracted, ok := ExtractErrorCode(err)
	if ok && extracted == string(code) {
		return true
	}
	if info, ok := ExtractErrorInfo(err); ok {
		return info.Reason == string(code)
	}
	return false
}

// Matcher pairs an error code with a callback for MatchError.
type Matcher[T any] struct {
	ErrorCode ErrorCode
	Fn        func() T
}

// MatchError is a switch-like error matcher. It tries each matcher in order
// against the error and invokes the first matching callback, returning its result.
// Matchers are evaluated in slice order, making matching deterministic.
// Returns the zero value of T and false if no matcher matches.
//
// Example:
//
//	result, ok := grpcerrors.MatchError(err, []grpcerrors.Matcher[string]{
//	    {ErrorCode: grpcerrors.ErrNotFound, Fn: func() string { return "not found" }},
//	    {ErrorCode: grpcerrors.ErrInvalidArgument, Fn: func() string { return "bad input" }},
//	})
func MatchError[T any](err error, matchers []Matcher[T]) (T, bool) {
	var zero T
	for _, m := range matchers {
		if MatchesError(err, m.ErrorCode) {
			return m.Fn(), true
		}
	}
	return zero, false
}

// SendTrailer attaches x-error-code and x-retryable gRPC trailing metadata to
// the server stream for the given error. It should be called from server-side
// handlers before returning the error, so that clients can read the trailers
// for fast inspection without parsing the protobuf ErrorInfo detail.
//
// This is a no-op if ctx is not a gRPC server stream context.
//
// Example:
//
//	if err != nil {
//	    grpcerrors.SendTrailer(ctx, err)
//	    return err
//	}
func SendTrailer(ctx context.Context, err error) {
	if ctx == nil {
		return
	}
	ge, ok := err.(*grpcError)
	if !ok || ge == nil {
		return
	}
	st := grpc.ServerTransportStreamFromContext(ctx)
	if st == nil {
		return
	}
	_ = st.SetTrailer(metadata.Pairs(
		TrailerKeyCode, ge.code,
		TrailerKeyRetryable, boolToString(ge.retryable),
	))
}

// statusCodeToHTTPStatus maps gRPC status codes to HTTP status codes.
// Used by ToProblemDetails for REST/HTTP adapters.
var statusCodeToHTTPStatus = map[codes.Code]int{
	codes.Canceled:           499,
	codes.Unknown:            500,
	codes.InvalidArgument:    400,
	codes.DeadlineExceeded:   504,
	codes.NotFound:           404,
	codes.AlreadyExists:      409,
	codes.PermissionDenied:   403,
	codes.ResourceExhausted:  429,
	codes.FailedPrecondition: 412,
	codes.Aborted:            409,
	codes.OutOfRange:         400,
	codes.Unimplemented:      501,
	codes.Internal:           500,
	codes.Unavailable:        503,
	codes.DataLoss:           500,
	codes.Unauthenticated:    401,
}

// HTTPStatusFromCode returns the HTTP status code for a gRPC status code.
// Useful for REST/HTTP adapters (e.g. grpc-gateway) that translate gRPC
// errors to HTTP responses.
func HTTPStatusFromCode(code codes.Code) int {
	if s, ok := statusCodeToHTTPStatus[code]; ok {
		return s
	}
	return 500
}

// Ensure compile-time that grpcError implements the interfaces expected by
// google.golang.org/grpc/status and errors.
var (
	_ interface{ GRPCStatus() *status.Status } = (*grpcError)(nil)
	_ error                                       = (*grpcError)(nil)
	_ error                                       = (*CodedError)(nil)
	_ ErrorCoder                                  = (*CodedError)(nil)
	_ ErrorCoder                                  = ErrorCode("")
)
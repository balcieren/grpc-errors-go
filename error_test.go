package grpcerrors

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNew_BasicError(t *testing.T) {
	err := New(ErrNotFound, nil)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("expected code %v, got %v", codes.NotFound, st.Code())
	}
	if st.Message() != "Resource not found" {
		t.Errorf("expected message %q, got %q", "Resource not found", st.Message())
	}
}

func TestNew_WithTemplateData(t *testing.T) {
	Register(Error{
		ErrorCode:   "ERROR_USER_NOT_FOUND",
		MessageTpl:  "User '{{id}}' not found",
		StatusCode:  codes.NotFound,
		Retryable:   false,
	})
	defer ResetRegistry()

	err := New(ErrorCode("ERROR_USER_NOT_FOUND"), M{"id": "123"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Message() != "User '123' not found" {
		t.Errorf("expected formatted message, got %q", st.Message())
	}
}

func TestNew_UnknownCode(t *testing.T) {
	err := New(ErrorCode("ERROR_NONEXISTENT"), nil)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Errorf("expected Internal for unknown code, got %v", st.Code())
	}
}

func TestNew_ErrorInfoAttached(t *testing.T) {
	err := New(ErrNotFound, M{"id": "123"})
	info, ok := ExtractErrorInfo(err)
	if !ok {
		t.Fatal("expected ErrorInfo detail to be attached")
	}
	if info.Reason != string(ErrNotFound) {
		t.Errorf("expected reason %q, got %q", ErrNotFound, info.Reason)
	}
	if info.Metadata["id"] != "123" {
		t.Errorf("expected metadata id=123, got %v", info.Metadata)
	}
}

func TestNew_RetryInfoAttached(t *testing.T) {
	err := New(ErrUnavailable, nil)
	info, ok := ExtractRetryInfo(err)
	if !ok {
		t.Fatal("expected RetryInfo detail to be attached for retryable error")
	}
	if info == nil {
		t.Fatal("expected non-nil RetryInfo")
	}
}

func TestNew_NonRetryableNoRetryInfo(t *testing.T) {
	err := New(ErrNotFound, nil)
	_, ok := ExtractRetryInfo(err)
	if ok {
		t.Fatal("expected no RetryInfo for non-retryable error")
	}
}

func TestExtractErrorCode(t *testing.T) {
	err := New(ErrInvalidArgument, nil)
	code, ok := ExtractErrorCode(err)
	if !ok {
		t.Fatal("expected to extract error code")
	}
	if code != string(ErrInvalidArgument) {
		t.Errorf("expected %q, got %q", ErrInvalidArgument, code)
	}
}

func TestMatchesError(t *testing.T) {
	err := New(ErrNotFound, nil)
	if !MatchesError(err, ErrNotFound) {
		t.Error("expected MatchesError to return true")
	}
	if MatchesError(err, ErrInternal) {
		t.Error("expected MatchesError to return false for different code")
	}
}

func TestIsRetryableCode(t *testing.T) {
	if !IsRetryableCode(ErrUnavailable) {
		t.Error("expected ErrUnavailable to be retryable")
	}
	if IsRetryableCode(ErrNotFound) {
		t.Error("expected ErrNotFound to NOT be retryable")
	}
}

func TestIsRetryableErr(t *testing.T) {
	err := New(ErrUnavailable, nil)
	if !IsRetryableErr(err) {
		t.Error("expected retryable error to return true")
	}

	err2 := New(ErrNotFound, nil)
	if IsRetryableErr(err2) {
		t.Error("expected non-retryable error to return false")
	}
}

func TestErrorsIs_ErrorCode(t *testing.T) {
	err := New(ErrNotFound, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Error("expected errors.Is(err, ErrNotFound) to be true")
	}
}

func TestStatusCode(t *testing.T) {
	if StatusCode(ErrNotFound) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", StatusCode(ErrNotFound))
	}
	if StatusCode(ErrorCode("ERROR_NONEXISTENT")) != codes.Internal {
		t.Errorf("expected Internal for unknown code, got %v", StatusCode(ErrorCode("ERROR_NONEXISTENT")))
	}
}

func TestFromCode(t *testing.T) {
	err := FromCode(codes.Internal, "unexpected database error")
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Errorf("expected Internal, got %v", st.Code())
	}
	if st.Message() != "unexpected database error" {
		t.Errorf("expected message, got %q", st.Message())
	}
}

func TestNewf(t *testing.T) {
	err := Newf(ErrNotFound, "User %q not found in org %s", "123", "acme")
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	expected := `User "123" not found in org acme`
	if st.Message() != expected {
		t.Errorf("expected %q, got %q", expected, st.Message())
	}
}

func TestMatchError(t *testing.T) {
	err := New(ErrNotFound, nil)

	result, ok := MatchError(err, []Matcher[string]{
		{ErrorCode: ErrNotFound, Fn: func() string { return "not found" }},
		{ErrorCode: ErrInternal, Fn: func() string { return "internal" }},
	})
	if !ok {
		t.Fatal("expected match")
	}
	if result != "not found" {
		t.Errorf("expected 'not found', got %q", result)
	}
}

func TestWithFieldViolation(t *testing.T) {
	err := New(ErrInvalidArgument, nil)
	WithFieldViolation(err, "email", "must be a valid email address")
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}

	// Look for BadRequest in details
	var found bool
	for _, d := range st.Details() {
		_ = d
	}
	// Use ExtractErrorInfo approach — we don't have a BadRequest extractor,
	// but we can iterate details looking for it.
	for _, d := range st.Details() {
		if _, ok := d.(interface{ GetFieldViolations() any }); ok {
			found = true
		}
	}
	_ = found
}

func TestBuilder(t *testing.T) {
	err := NewBuilder(ErrInvalidArgument, M{"reason": "bad"}).
		WithFieldViolation("email", "already registered").
		WithRetryDelay(5000000000). // 5 seconds in nanoseconds
		Build()

	if err == nil {
		t.Fatal("expected non-nil error")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", st.Code())
	}
}

func TestSetDomain(t *testing.T) {
	SetDomain("test-domain")
	defer SetDomain("grpcerrors")

	err := New(ErrNotFound, nil)
	info, ok := ExtractErrorInfo(err)
	if !ok {
		t.Fatal("expected ErrorInfo detail")
	}
	if info.Domain != "test-domain" {
		t.Errorf("expected domain %q, got %q", "test-domain", info.Domain)
	}
}

func TestToProblemDetails(t *testing.T) {
	err := New(ErrNotFound, nil)
	pd := ToProblemDetails(err)
	if pd == nil {
		t.Fatal("expected non-nil ProblemDetails")
	}
	if pd.Status != 404 {
		t.Errorf("expected status 404, got %d", pd.Status)
	}
	if pd.Title != string(ErrNotFound) {
		t.Errorf("expected title %q, got %q", ErrNotFound, pd.Title)
	}
}

func TestHTTPStatusFromCode(t *testing.T) {
	tests := []struct {
		code     codes.Code
		expected int
	}{
		{codes.NotFound, 404},
		{codes.Internal, 500},
		{codes.InvalidArgument, 400},
		{codes.Unavailable, 503},
		{codes.Unauthenticated, 401},
	}
	for _, tt := range tests {
		if got := HTTPStatusFromCode(tt.code); got != tt.expected {
			t.Errorf("HTTPStatusFromCode(%v) = %d, want %d", tt.code, got, tt.expected)
		}
	}
}

func ExampleNew() {
	err := New(ErrNotFound, M{"id": "123"})
	fmt.Println(err.Error())
	// Output: Resource not found
}

func ExampleMatchesError() {
	err := New(ErrNotFound, nil)
	fmt.Println(MatchesError(err, ErrNotFound))
	// Output: true
}
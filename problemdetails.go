package grpcerrors

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProblemDetails represents an RFC 7807 (Problem Details for HTTP APIs) error response.
// It is useful for REST/HTTP adapters (e.g. grpc-gateway) that need to translate
// gRPC errors to JSON.
type ProblemDetails struct {
	// Type is a URI reference that identifies the problem type.
	// Defaults to "about:blank" if not set.
	Type string `json:"type,omitempty"`

	// Title is a short, human-readable summary of the problem type.
	// It SHOULD NOT change from occurrence to occurrence of the problem.
	Title string `json:"title,omitempty"`

	// Detail is a human-readable explanation specific to this occurrence.
	Detail string `json:"detail,omitempty"`

	// Status is the HTTP status code for this occurrence.
	Status int `json:"status,omitempty"`

	// Instance is a URI reference that identifies the specific occurrence.
	Instance string `json:"instance,omitempty"`
}

// ToProblemDetails converts a gRPC error to an RFC 7807 ProblemDetails response.
// It extracts the error code, message, and HTTP status from the gRPC error.
// Returns nil if the error is not a gRPC error.
//
// Example:
//
//	err := gerr.New(gerr.ErrNotFound, nil)
//	pd := gerr.ToProblemDetails(err)
//	// pd.Status = 404, pd.Title = "ERROR_NOT_FOUND", pd.Detail = "Resource not found"
func ToProblemDetails(err error) *ProblemDetails {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return nil
	}

	pd := &ProblemDetails{
		Type:   "about:blank",
		Detail: st.Message(),
		Status: 500,
	}

	if httpStatus, ok := statusCodeToHTTPStatus[st.Code()]; ok {
		pd.Status = httpStatus
	}

	// Try to extract domain error code for the title
	if code, ok := ExtractErrorCode(err); ok {
		pd.Title = code
	}

	return pd
}

// HTTPStatusFromGRPCCode returns the HTTP status code for a gRPC code.
// Useful for REST/HTTP adapters (e.g. grpc-gateway) that translate gRPC
// errors to HTTP responses.
//
// Deprecated: Use HTTPStatusFromCode instead.
func HTTPStatusFromGRPCCode(code codes.Code) int {
	return HTTPStatusFromCode(code)
}
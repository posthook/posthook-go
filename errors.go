package posthook

import "fmt"

// Error is the base error type returned by the Posthook API.
type Error struct {
	StatusCode int    `json:"statusCode"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("posthook: %s (status %d, code %s)", e.Message, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("posthook: %s (status %d)", e.Message, e.StatusCode)
}

// BadRequestError is returned for HTTP 400 responses.
type BadRequestError struct{ Err *Error }

func (e *BadRequestError) Error() string { return e.Err.Error() }
func (e *BadRequestError) Unwrap() error { return e.Err }

// AuthenticationError is returned for HTTP 401 responses.
type AuthenticationError struct{ Err *Error }

func (e *AuthenticationError) Error() string { return e.Err.Error() }
func (e *AuthenticationError) Unwrap() error { return e.Err }

// ForbiddenError is returned for HTTP 403 responses.
type ForbiddenError struct{ Err *Error }

func (e *ForbiddenError) Error() string { return e.Err.Error() }
func (e *ForbiddenError) Unwrap() error { return e.Err }

// NotFoundError is returned for HTTP 404 responses.
type NotFoundError struct{ Err *Error }

func (e *NotFoundError) Error() string { return e.Err.Error() }
func (e *NotFoundError) Unwrap() error { return e.Err }

// PayloadTooLargeError is returned for HTTP 413 responses.
type PayloadTooLargeError struct{ Err *Error }

func (e *PayloadTooLargeError) Error() string { return e.Err.Error() }
func (e *PayloadTooLargeError) Unwrap() error { return e.Err }

// RateLimitError is returned for HTTP 429 responses.
type RateLimitError struct{ Err *Error }

func (e *RateLimitError) Error() string { return e.Err.Error() }
func (e *RateLimitError) Unwrap() error { return e.Err }

// InternalServerError is returned for HTTP 5xx responses.
type InternalServerError struct{ Err *Error }

func (e *InternalServerError) Error() string { return e.Err.Error() }
func (e *InternalServerError) Unwrap() error { return e.Err }

// ConnectionError is returned for network or timeout errors.
type ConnectionError struct{ Err *Error }

func (e *ConnectionError) Error() string { return e.Err.Error() }
func (e *ConnectionError) Unwrap() error { return e.Err }

// SignatureVerificationError is returned when webhook signature verification fails.
type SignatureVerificationError struct{ Err *Error }

func (e *SignatureVerificationError) Error() string { return e.Err.Error() }
func (e *SignatureVerificationError) Unwrap() error  { return e.Err }

// newError creates the appropriate typed error for the given HTTP status code.
func newError(statusCode int, message string) error {
	base := &Error{
		StatusCode: statusCode,
		Message:    message,
	}

	switch statusCode {
	case 400:
		return &BadRequestError{base}
	case 401:
		return &AuthenticationError{base}
	case 403:
		return &ForbiddenError{base}
	case 404:
		return &NotFoundError{base}
	case 413:
		return &PayloadTooLargeError{base}
	case 429:
		return &RateLimitError{base}
	case 0:
		return &ConnectionError{base}
	default:
		if statusCode >= 500 {
			return &InternalServerError{base}
		}
		return base
	}
}

// newSignatureError creates a SignatureVerificationError.
func newSignatureError(message string) error {
	return &SignatureVerificationError{
		Err: &Error{
			Message: message,
		},
	}
}

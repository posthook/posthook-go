package posthook

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestError_Error(t *testing.T) {
	e := &Error{StatusCode: 400, Message: "bad request"}
	assert.Equal(t, "posthook: bad request (status 400)", e.Error())
}

func TestError_ErrorWithCode(t *testing.T) {
	e := &Error{StatusCode: 400, Code: "INVALID_PARAM", Message: "bad request"}
	assert.Equal(t, "posthook: bad request (status 400, code INVALID_PARAM)", e.Error())
}

func TestNewError_BadRequest(t *testing.T) {
	err := newError(400, "invalid path")
	var target *BadRequestError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 400, target.Err.StatusCode)
	assert.Equal(t, "invalid path", target.Err.Message)
}

func TestNewError_Authentication(t *testing.T) {
	err := newError(401, "unauthorized")
	var target *AuthenticationError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 401, target.Err.StatusCode)
}

func TestNewError_Forbidden(t *testing.T) {
	err := newError(403, "forbidden")
	var target *ForbiddenError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 403, target.Err.StatusCode)
}

func TestNewError_NotFound(t *testing.T) {
	err := newError(404, "not found")
	var target *NotFoundError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 404, target.Err.StatusCode)
}

func TestNewError_PayloadTooLarge(t *testing.T) {
	err := newError(413, "too large")
	var target *PayloadTooLargeError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 413, target.Err.StatusCode)
}

func TestNewError_RateLimit(t *testing.T) {
	err := newError(429, "rate limited")
	var target *RateLimitError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 429, target.Err.StatusCode)
}

func TestNewError_InternalServer(t *testing.T) {
	err := newError(500, "internal error")
	var target *InternalServerError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 500, target.Err.StatusCode)
}

func TestNewError_InternalServer502(t *testing.T) {
	err := newError(502, "bad gateway")
	var target *InternalServerError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 502, target.Err.StatusCode)
}

func TestNewError_Connection(t *testing.T) {
	err := newError(0, "connection refused")
	var target *ConnectionError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "connection refused", target.Err.Message)
}

func TestNewError_UnknownStatusReturnsBaseError(t *testing.T) {
	err := newError(418, "i'm a teapot")
	var target *Error
	require.True(t, errors.As(err, &target))
	assert.Equal(t, 418, target.StatusCode)

	// Should not match specific typed errors.
	var badReq *BadRequestError
	assert.False(t, errors.As(err, &badReq))
}

func TestTypedError_UnwrapsToBaseError(t *testing.T) {
	err := newError(401, "unauthorized")
	var base *Error
	require.True(t, errors.As(err, &base))
	assert.Equal(t, 401, base.StatusCode)
	assert.Equal(t, "unauthorized", base.Message)
}

func TestNewSignatureError(t *testing.T) {
	err := newSignatureError("bad signature")
	var target *SignatureVerificationError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "bad signature", target.Err.Message)
	assert.Contains(t, err.Error(), "bad signature")
}

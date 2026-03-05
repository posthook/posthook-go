package posthook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callbackServer creates an httptest.Server that returns the given status code and body.
func callbackServer(t *testing.T, statusCode int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if body != nil {
			json.NewEncoder(w).Encode(body)
		}
	}))
}

func TestAck_Success(t *testing.T) {
	srv := callbackServer(t, 200, map[string]any{"data": map[string]any{"status": "completed"}})
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	result, err := d.Ack(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.Applied)
	assert.Equal(t, "completed", result.Status)
}

func TestAck_SuccessNotApplied(t *testing.T) {
	// Server returns 200 but status is "nacked" (idempotent no-op for ack).
	srv := callbackServer(t, 200, map[string]any{"data": map[string]any{"status": "nacked"}})
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	result, err := d.Ack(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.Equal(t, "nacked", result.Status)
}

func TestAck_404NotFound(t *testing.T) {
	srv := callbackServer(t, 404, nil)
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	result, err := d.Ack(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.Equal(t, "not_found", result.Status)
}

func TestAck_409Conflict(t *testing.T) {
	srv := callbackServer(t, 409, nil)
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	result, err := d.Ack(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.Equal(t, "conflict", result.Status)
}

func TestAck_401Throws(t *testing.T) {
	srv := callbackServer(t, 401, nil)
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	_, err := d.Ack(context.Background(), nil)
	require.Error(t, err)

	var cbErr *CallbackError
	assert.True(t, errors.As(err, &cbErr))
	assert.Equal(t, 401, cbErr.Err.StatusCode)
}

func TestAck_410Throws(t *testing.T) {
	srv := callbackServer(t, 410, nil)
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	_, err := d.Ack(context.Background(), nil)
	require.Error(t, err)

	var cbErr *CallbackError
	assert.True(t, errors.As(err, &cbErr))
	assert.Equal(t, 410, cbErr.Err.StatusCode)
	assert.Contains(t, cbErr.Error(), "ack failed: 410")
}

func TestAck_500Throws(t *testing.T) {
	srv := callbackServer(t, 500, nil)
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	_, err := d.Ack(context.Background(), nil)
	require.Error(t, err)

	var cbErr *CallbackError
	assert.True(t, errors.As(err, &cbErr))
	assert.Contains(t, cbErr.Error(), "ack failed: 500")
}

func TestAck_EmptyURL(t *testing.T) {
	d := &Delivery{}
	_, err := d.Ack(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ack URL is empty")
}

func TestNack_Success(t *testing.T) {
	srv := callbackServer(t, 200, map[string]any{"data": map[string]any{"status": "nacked"}})
	defer srv.Close()

	d := &Delivery{NackURL: srv.URL}
	result, err := d.Nack(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, result.Applied)
	assert.Equal(t, "nacked", result.Status)
}

func TestNack_SuccessNotApplied(t *testing.T) {
	srv := callbackServer(t, 200, map[string]any{"data": map[string]any{"status": "completed"}})
	defer srv.Close()

	d := &Delivery{NackURL: srv.URL}
	result, err := d.Nack(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, result.Applied)
	assert.Equal(t, "completed", result.Status)
}

func TestNack_EmptyURL(t *testing.T) {
	d := &Delivery{}
	_, err := d.Nack(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nack URL is empty")
}

func TestNack_410Throws(t *testing.T) {
	srv := callbackServer(t, 410, nil)
	defer srv.Close()

	d := &Delivery{NackURL: srv.URL}
	_, err := d.Nack(context.Background(), nil)
	require.Error(t, err)

	var cbErr *CallbackError
	assert.True(t, errors.As(err, &cbErr))
	assert.Equal(t, 410, cbErr.Err.StatusCode)
	assert.Contains(t, cbErr.Error(), "nack failed: 410")
}

func TestAck_JSONBody(t *testing.T) {
	// Verify JSON body is serialized and Content-Type is set.
	var capturedBody []byte
	var capturedContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedContentType = r.Header.Get("Content-Type")
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "completed"}})
	}))
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	_, err := d.Ack(context.Background(), map[string]any{"done": true})
	require.NoError(t, err)

	assert.Equal(t, "application/json", capturedContentType)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &parsed))
	assert.Equal(t, true, parsed["done"])
}

func TestAck_NilBodyNoContentType(t *testing.T) {
	var capturedContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "completed"}})
	}))
	defer srv.Close()

	d := &Delivery{AckURL: srv.URL}
	_, err := d.Ack(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, capturedContentType)
}

func TestAck_WithCustomClient(t *testing.T) {
	srv := callbackServer(t, 200, map[string]any{"data": map[string]any{"status": "completed"}})
	defer srv.Close()

	customClient := &http.Client{}
	d := &Delivery{AckURL: srv.URL}
	result, err := d.Ack(context.Background(), nil, WithCallbackClient(customClient))
	require.NoError(t, err)
	assert.True(t, result.Applied)
}

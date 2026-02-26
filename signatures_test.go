package posthook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// computeTestSignature computes a Posthook signature for testing purposes.
// This matches the algorithm in poster.go:computePosthookSignature.
func computeTestSignature(key string, timestamp int64, body []byte) string {
	signedPayload := fmt.Appendf(nil, "%d.", timestamp)
	signedPayload = append(signedPayload, body...)

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(signedPayload)
	return "v1," + hex.EncodeToString(mac.Sum(nil))
}

func makeHeaders(id string, timestamp int64, signature string) http.Header {
	h := http.Header{}
	h.Set("Posthook-Id", id)
	h.Set("Posthook-Timestamp", fmt.Sprintf("%d", timestamp))
	h.Set("Posthook-Signature", signature)
	return h
}

func TestSignatures_SingleKey(t *testing.T) {
	key := "test-secret-key"
	timestamp := int64(1700000000)
	body := []byte(`{"id":"hook-1","path":"/webhooks/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"event":"test"},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-1", timestamp, sig)

	svc := &SignaturesService{signingKey: key}
	now := time.Unix(timestamp+10, 0)
	delivery, err := svc.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.NoError(t, err)
	assert.Equal(t, "hook-1", delivery.HookID)
	assert.Equal(t, timestamp, delivery.Timestamp)
	assert.Equal(t, "/webhooks/test", delivery.Path)
	assert.Equal(t, body, delivery.Body)
}

func TestSignatures_MultiKeyRotation(t *testing.T) {
	activeKey := "new-secret-key"
	retiringKey := "old-secret-key"
	timestamp := int64(1700000000)
	body := []byte(`{"id":"hook-1","path":"/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	// Signature header contains space-separated signatures from both keys.
	sig1 := computeTestSignature(activeKey, timestamp, body)
	sig2 := computeTestSignature(retiringKey, timestamp, body)
	combinedSig := sig1 + " " + sig2
	headers := makeHeaders("hook-1", timestamp, combinedSig)

	// Verify with the retiring key — should match the second signature.
	svc := &SignaturesService{signingKey: retiringKey}
	now := time.Unix(timestamp+10, 0)
	delivery, err := svc.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.NoError(t, err)
	assert.Equal(t, "hook-1", delivery.HookID)

	// Also verify with the active key — should match the first signature.
	svc2 := &SignaturesService{signingKey: activeKey}
	delivery2, err := svc2.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.NoError(t, err)
	assert.Equal(t, "hook-1", delivery2.HookID)
}

// TestSignatures_ManualVerification matches TestComputePosthookSignature_VerifyManual
// from poster/signature_test.go to ensure cross-language compatibility.
func TestSignatures_ManualVerification(t *testing.T) {
	secretKey := "my-webhook-secret"
	timestamp := int64(1700000000)
	body := []byte(`{"user_id":123}`)

	// Manually compute expected signature.
	signedPayload := fmt.Sprintf("%d.%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(signedPayload))
	expectedSig := "v1," + hex.EncodeToString(mac.Sum(nil))

	// Verify our helper produces the same result.
	assert.Equal(t, expectedSig, computeTestSignature(secretKey, timestamp, body))

	// Now verify ParseDelivery accepts it.
	// Wrap in a delivery payload envelope.
	deliveryBody := []byte(`{"id":"hook-manual","path":"/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"user_id":123},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	deliverySig := computeTestSignature(secretKey, timestamp, deliveryBody)
	headers := makeHeaders("hook-manual", timestamp, deliverySig)

	svc := &SignaturesService{signingKey: secretKey}
	now := time.Unix(timestamp+10, 0)
	delivery, err := svc.ParseDelivery(deliveryBody, headers, withNow(func() time.Time { return now }))
	require.NoError(t, err)
	assert.Equal(t, "hook-manual", delivery.HookID)
}

func TestSignatures_Deterministic(t *testing.T) {
	key := "test-key"
	timestamp := int64(1700000000)
	body := []byte(`{"event":"test"}`)

	sig1 := computeTestSignature(key, timestamp, body)
	sig2 := computeTestSignature(key, timestamp, body)
	assert.Equal(t, sig1, sig2)
}

func TestSignatures_DifferentTimestamps(t *testing.T) {
	key := "test-key"
	body := []byte(`{"event":"test"}`)

	sig1 := computeTestSignature(key, 1700000000, body)
	sig2 := computeTestSignature(key, 1700000001, body)
	assert.NotEqual(t, sig1, sig2)
}

func TestSignatures_HexEncodedLength(t *testing.T) {
	sig := computeTestSignature("key", 1700000000, []byte("body"))
	// Format: "v1," + 64 hex chars (SHA256 = 32 bytes = 64 hex chars)
	assert.Len(t, sig, 3+64)
	assert.True(t, len(sig) > 3)
	hexPart := sig[3:]
	assert.Len(t, hexPart, 64)
}

func TestSignatures_MissingHeaders(t *testing.T) {
	svc := &SignaturesService{signingKey: "key"}

	tests := []struct {
		name    string
		headers http.Header
		errMsg  string
	}{
		{
			name:    "missing Posthook-Timestamp",
			headers: func() http.Header { h := http.Header{}; h.Set("Posthook-Id", "id"); h.Set("Posthook-Signature", "v1,abc"); return h }(),
			errMsg:  "missing Posthook-Timestamp header",
		},
		{
			name:    "missing Posthook-Signature",
			headers: func() http.Header { h := http.Header{}; h.Set("Posthook-Id", "id"); h.Set("Posthook-Timestamp", "123"); return h }(),
			errMsg:  "missing Posthook-Signature header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ParseDelivery([]byte("{}"), tt.headers)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestSignatures_ExpiredTimestamp(t *testing.T) {
	key := "test-key"
	timestamp := int64(1700000000)
	body := []byte(`{"id":"h","path":"/t","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("h", timestamp, sig)

	svc := &SignaturesService{signingKey: key}
	// Now is 10 minutes after timestamp — exceeds 5 min default tolerance.
	now := time.Unix(timestamp+600, 0)
	_, err := svc.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp too old")
}

func TestSignatures_TamperedBody(t *testing.T) {
	key := "test-key"
	timestamp := int64(1700000000)
	originalBody := []byte(`{"id":"h","path":"/t","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"original":true},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	tamperedBody := []byte(`{"id":"h","path":"/t","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"tampered":true},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(key, timestamp, originalBody)
	headers := makeHeaders("h", timestamp, sig)

	svc := &SignaturesService{signingKey: key}
	now := time.Unix(timestamp+10, 0)
	_, err := svc.ParseDelivery(tamperedBody, headers, withNow(func() time.Time { return now }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature verification failed")
}

func TestSignatures_WrongKey(t *testing.T) {
	correctKey := "correct-key"
	wrongKey := "wrong-key"
	timestamp := int64(1700000000)
	body := []byte(`{"id":"h","path":"/t","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(correctKey, timestamp, body)
	headers := makeHeaders("h", timestamp, sig)

	svc := &SignaturesService{signingKey: wrongKey}
	now := time.Unix(timestamp+10, 0)
	_, err := svc.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature verification failed")
}

func TestSignatures_CustomTolerance(t *testing.T) {
	key := "test-key"
	timestamp := int64(1700000000)
	body := []byte(`{"id":"h","path":"/t","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("h", timestamp, sig)

	svc := &SignaturesService{signingKey: key}
	// 10 minutes after — would fail with default 5min tolerance.
	now := time.Unix(timestamp+600, 0)

	// With 15 minute tolerance, it should pass.
	delivery, err := svc.ParseDelivery(body, headers,
		WithTolerance(15*time.Minute),
		withNow(func() time.Time { return now }),
	)
	require.NoError(t, err)
	assert.Equal(t, "h", delivery.HookID)
}

func TestSignatures_NoSigningKey(t *testing.T) {
	svc := &SignaturesService{}
	_, err := svc.ParseDelivery([]byte("{}"), http.Header{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing key is required")
}

func TestSignatures_ParsesDeliveryFields(t *testing.T) {
	key := "test-key"
	timestamp := int64(1700000000)
	body := []byte(`{"id":"hook-parse","path":"/webhooks/user-created","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"userId":"123","event":"user.created"},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-parse", timestamp, sig)

	svc := &SignaturesService{signingKey: key}
	now := time.Unix(timestamp+10, 0)
	delivery, err := svc.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.NoError(t, err)

	assert.Equal(t, "hook-parse", delivery.HookID)
	assert.Equal(t, timestamp, delivery.Timestamp)
	assert.Equal(t, "/webhooks/user-created", delivery.Path)
	assert.Equal(t, time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC), delivery.PostAt)
	assert.Equal(t, time.Date(2026, 2, 22, 15, 0, 1, 0, time.UTC), delivery.PostedAt)
	assert.Equal(t, time.Date(2026, 2, 22, 14, 55, 0, 0, time.UTC), delivery.CreatedAt)
	assert.Equal(t, time.Date(2026, 2, 22, 14, 55, 0, 0, time.UTC), delivery.UpdatedAt)
	assert.Equal(t, body, delivery.Body)

	// Data should be accessible as json.RawMessage.
	assert.Contains(t, string(delivery.Data), `"userId":"123"`)
}

func TestSignatures_ErrorType(t *testing.T) {
	svc := &SignaturesService{signingKey: "key"}
	headers := makeHeaders("id", 100, "v1,bad")

	now := time.Unix(110, 0)
	_, err := svc.ParseDelivery([]byte(`{"id":"x","path":"/t","postAt":"","postedAt":"","data":{},"createdAt":"","updatedAt":""}`), headers, withNow(func() time.Time { return now }))
	require.Error(t, err)

	var sigErr *SignatureVerificationError
	assert.True(t, errors.As(err, &sigErr))
}

func TestNewSignatures_NoKeyReturnsError(t *testing.T) {
	t.Setenv("POSTHOOK_SIGNING_KEY", "")

	_, err := NewSignatures("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing key is required")
}

func TestNewSignatures_EnvVarFallback(t *testing.T) {
	t.Setenv("POSTHOOK_SIGNING_KEY", "env-signing-key")

	sigs, err := NewSignatures("")
	require.NoError(t, err)
	require.NotNil(t, sigs)
	assert.Equal(t, "env-signing-key", sigs.signingKey)
}

func TestNewSignatures_ExplicitKeyTakesPrecedence(t *testing.T) {
	t.Setenv("POSTHOOK_SIGNING_KEY", "env-signing-key")

	sigs, err := NewSignatures("explicit-signing-key")
	require.NoError(t, err)
	require.NotNil(t, sigs)
	assert.Equal(t, "explicit-signing-key", sigs.signingKey)
}

func TestNewSignatures_StandaloneUsage(t *testing.T) {
	key := "ph_sk_standalone_test"

	// Create a standalone SignaturesService without any Client.
	sigs, err := NewSignatures(key)
	require.NoError(t, err)
	require.NotNil(t, sigs)

	timestamp := int64(1700000000)
	body := []byte(`{"id":"hook-standalone","path":"/webhooks/standalone","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"source":"standalone"},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)

	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-standalone", timestamp, sig)

	now := time.Unix(timestamp+10, 0)
	delivery, err := sigs.ParseDelivery(body, headers, withNow(func() time.Time { return now }))
	require.NoError(t, err)

	assert.Equal(t, "hook-standalone", delivery.HookID)
	assert.Equal(t, timestamp, delivery.Timestamp)
	assert.Equal(t, "/webhooks/standalone", delivery.Path)
	assert.Contains(t, string(delivery.Data), `"source":"standalone"`)
	assert.Equal(t, body, delivery.Body)
}

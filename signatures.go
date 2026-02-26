package posthook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultTolerance = 5 * time.Minute

// SignaturesService provides webhook signature verification.
type SignaturesService struct {
	signingKey string
}

// NewSignatures creates a standalone SignaturesService for verifying webhook
// signatures without needing a full Client or API key. This is useful when
// you only need to receive and verify webhooks, not schedule hooks.
//
// If signingKey is empty, it falls back to the POSTHOOK_SIGNING_KEY
// environment variable. An error is returned if no signing key is available
// from either source.
//
//	sigs, err := posthook.NewSignatures("whsec_your_signing_key")
//	delivery, err := sigs.ParseDelivery(body, req.Header)
func NewSignatures(signingKey string) (*SignaturesService, error) {
	if signingKey == "" {
		signingKey = os.Getenv("POSTHOOK_SIGNING_KEY")
	}
	if signingKey == "" {
		return nil, fmt.Errorf("posthook: signing key is required (pass explicitly or set POSTHOOK_SIGNING_KEY)")
	}
	return &SignaturesService{signingKey: signingKey}, nil
}

// VerifyOption configures signature verification behavior.
type VerifyOption func(*verifyConfig)

type verifyConfig struct {
	tolerance  time.Duration
	signingKey string
	now        func() time.Time // for testing
}

// WithTolerance sets the maximum age of a webhook signature. Signatures older
// than this duration are rejected. The default is 5 minutes.
func WithTolerance(d time.Duration) VerifyOption {
	return func(c *verifyConfig) {
		c.tolerance = d
	}
}

// withNow overrides the current time function (for testing).
func withNow(fn func() time.Time) VerifyOption {
	return func(c *verifyConfig) {
		c.now = fn
	}
}

// ParseDelivery verifies the webhook signature from the provided headers and
// parses the delivery payload. It extracts the Posthook-Id, Posthook-Timestamp,
// and Posthook-Signature headers, validates the HMAC-SHA256 signature, and
// returns a parsed Delivery.
//
// The body parameter should be the raw HTTP request body bytes.
func (s *SignaturesService) ParseDelivery(body []byte, headers http.Header, opts ...VerifyOption) (*Delivery, error) {
	cfg := &verifyConfig{
		tolerance:  defaultTolerance,
		signingKey: s.signingKey,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.signingKey == "" {
		return nil, newSignatureError("signing key is required: pass to NewSignatures() or use WithSigningKey() on the client")
	}

	hookID := headers.Get("Posthook-Id")

	timestampStr := headers.Get("Posthook-Timestamp")
	if timestampStr == "" {
		return nil, newSignatureError("missing Posthook-Timestamp header")
	}

	signature := headers.Get("Posthook-Signature")
	if signature == "" {
		return nil, newSignatureError("missing Posthook-Signature header")
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return nil, newSignatureError(fmt.Sprintf("invalid Posthook-Timestamp: %s", timestampStr))
	}

	// Validate timestamp tolerance.
	now := cfg.now()
	diff := now.Unix() - timestamp
	if diff < 0 {
		diff = -diff
	}
	toleranceSecs := int64(math.Ceil(cfg.tolerance.Seconds()))
	if diff > toleranceSecs {
		return nil, newSignatureError(fmt.Sprintf(
			"timestamp too old: %d seconds difference exceeds %s tolerance",
			now.Unix()-timestamp, cfg.tolerance,
		))
	}

	// Compute expected signature: HMAC-SHA256("{timestamp}.{body}", signingKey)
	signedPayload := fmt.Appendf(nil, "%d.", timestamp)
	signedPayload = append(signedPayload, body...)

	mac := hmac.New(sha256.New, []byte(cfg.signingKey))
	mac.Write(signedPayload)
	expectedSig := "v1," + hex.EncodeToString(mac.Sum(nil))

	// Check against each space-separated signature (supports key rotation).
	signatures := strings.Split(signature, " ")
	verified := false
	for _, sig := range signatures {
		if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) == 1 {
			verified = true
			break
		}
	}

	if !verified {
		return nil, newSignatureError("signature verification failed")
	}

	// Parse the delivery payload.
	var payload deliveryPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, newSignatureError(fmt.Sprintf("failed to parse delivery payload: %s", err))
	}

	postAt, _ := time.Parse(time.RFC3339, payload.PostAt)
	postedAt, _ := time.Parse(time.RFC3339, payload.PostedAt)
	createdAt, _ := time.Parse(time.RFC3339, payload.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, payload.UpdatedAt)

	return &Delivery{
		HookID:    hookID,
		Timestamp: timestamp,
		Path:      payload.Path,
		Data:      payload.Data,
		Body:      body,
		PostAt:    postAt,
		PostedAt:  postedAt,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

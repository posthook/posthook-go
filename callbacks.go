package posthook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CallbackOption configures callback behavior for Ack and Nack.
type CallbackOption func(*callbackConfig)

type callbackConfig struct {
	client *http.Client
}

// WithCallbackClient sets a custom HTTP client for the callback request.
// By default, http.DefaultClient is used.
func WithCallbackClient(client *http.Client) CallbackOption {
	return func(c *callbackConfig) {
		c.client = client
	}
}

// Ack acknowledges async processing completion. The hook is marked as
// completed (or stays in its current state if already resolved).
//
// Returns a CallbackResult indicating whether the ack was applied.
// Returns an error if AckURL is empty or if the request fails unexpectedly.
//
//	delivery, _ := sigs.ParseDelivery(body, r.Header)
//	if delivery.AckURL != "" {
//	    result, err := delivery.Ack(ctx, map[string]any{"done": true})
//	}
func (d *Delivery) Ack(ctx context.Context, body any, opts ...CallbackOption) (*CallbackResult, error) {
	if d.AckURL == "" {
		return nil, fmt.Errorf("posthook: ack URL is empty (delivery has no async callback headers)")
	}
	return doCallback(ctx, d.AckURL, body, "ack", "completed", opts)
}

// Nack sends a negative acknowledgement. The hook is retried or marked as
// failed based on your project's retry settings.
//
// Returns a CallbackResult indicating whether the nack was applied.
// Returns an error if NackURL is empty or if the request fails unexpectedly.
//
//	result, err := delivery.Nack(ctx, map[string]any{"reason": "processing failed"})
func (d *Delivery) Nack(ctx context.Context, body any, opts ...CallbackOption) (*CallbackResult, error) {
	if d.NackURL == "" {
		return nil, fmt.Errorf("posthook: nack URL is empty (delivery has no async callback headers)")
	}
	return doCallback(ctx, d.NackURL, body, "nack", "nacked", opts)
}

func doCallback(ctx context.Context, url string, body any, action, expectedStatus string, opts []CallbackOption) (*CallbackResult, error) {
	cfg := &callbackConfig{client: http.DefaultClient}
	for _, opt := range opts {
		opt(cfg)
	}

	var reqBody io.Reader
	var contentType string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("posthook: failed to marshal %s body: %w", action, err)
		}
		reqBody = bytes.NewReader(data)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("posthook: failed to create %s request: %w", action, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posthook: %s request failed: %w", action, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var envelope struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		status := "unknown"
		if json.Unmarshal(respBody, &envelope) == nil && envelope.Data.Status != "" {
			status = envelope.Data.Status
		}
		return &CallbackResult{Applied: status == expectedStatus, Status: status}, nil
	}

	if resp.StatusCode == 404 {
		return &CallbackResult{Applied: false, Status: "not_found"}, nil
	}
	if resp.StatusCode == 409 {
		return &CallbackResult{Applied: false, Status: "conflict"}, nil
	}

	msg := fmt.Sprintf("%s failed: %d", action, resp.StatusCode)
	if len(respBody) > 0 {
		msg += ": " + string(respBody)
	}
	return nil, &CallbackError{
		Err: &Error{
			StatusCode: resp.StatusCode,
			Message:    msg,
			Code:       "callback_error",
		},
	}
}

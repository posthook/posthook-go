package posthook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"time"
)

// Version is the semantic version of this SDK.
const Version = "1.0.0"

const defaultBaseURL = "https://api.posthook.io"

var defaultUserAgent = "posthook-go/" + Version + " (" + runtime.Version() + "; " + runtime.GOOS + ")"

// Client manages communication with the Posthook API.
// A Client is safe for concurrent use by multiple goroutines.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	apiKey     string
	userAgent  string
	common     service

	// Hooks provides access to hook scheduling and management endpoints.
	Hooks *HooksService

	// Signatures provides webhook signature verification.
	Signatures *SignaturesService
}

type service struct {
	client *Client
}

// Response wraps the standard http.Response and includes Posthook-specific
// metadata such as quota information. Note that the response body has already
// been read and closed; do not attempt to read from it.
type Response struct {
	*http.Response
	Quota *QuotaInfo
}

// Option configures a Client.
type Option func(*Client) error

// WithBaseURL sets a custom base URL for API requests.
func WithBaseURL(rawURL string) Option {
	return func(c *Client) error {
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("posthook: invalid base URL: %w", err)
		}
		c.baseURL = u
		return nil
	}
}

// WithHTTPClient sets a custom HTTP client for API requests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		c.httpClient = hc
		return nil
	}
}

// WithUserAgent sets a custom User-Agent header for API requests.
func WithUserAgent(ua string) Option {
	return func(c *Client) error {
		c.userAgent = ua
		return nil
	}
}

// WithSigningKey sets the signing key used for webhook signature verification.
func WithSigningKey(key string) Option {
	return func(c *Client) error {
		if key == "" {
			return fmt.Errorf("posthook: signing key must not be empty")
		}
		c.Signatures.signingKey = key
		return nil
	}
}
// NewClient creates a new Posthook API client. If apiKey is empty, it falls
// back to the POSTHOOK_API_KEY environment variable. Returns an error if no
// API key is available.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv("POSTHOOK_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("posthook: API key is required (pass to NewClient or set POSTHOOK_API_KEY)")
	}

	baseURL, _ := url.Parse(defaultBaseURL)

	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    baseURL,
		apiKey:     apiKey,
		userAgent:  defaultUserAgent,
	}

	c.common.client = c
	c.Hooks = (*HooksService)(&c.common)
	c.Signatures = &SignaturesService{}

	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}

	return c, nil
}

// newRequest creates an API request. The body is JSON-encoded if non-nil.
func (c *Client) newRequest(method, path string, body any) (*http.Request, error) {
	u, err := c.baseURL.Parse(path)
	if err != nil {
		return nil, err
	}

	var buf io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("posthook: failed to marshal request body: %w", err)
		}
		buf = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

// dataEnvelope wraps the standard API response envelope.
type dataEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

// execute performs the HTTP request, returning the raw response body,
// the Response wrapper (with quota info), and any error.
func (c *Client) execute(ctx context.Context, req *http.Request) ([]byte, *Response, error) {
	resp, err := c.httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, &ConnectionError{Err: &Error{Message: err.Error()}}
	}

	response := &Response{Response: resp}
	parseQuota(resp, response)

	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("posthook: failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		msg := http.StatusText(resp.StatusCode)
		var env dataEnvelope
		if json.Unmarshal(respBody, &env) == nil && env.Error != "" {
			msg = env.Error
		}
		return nil, response, newError(resp.StatusCode, msg)
	}

	return respBody, response, nil
}

// do executes a request and unmarshals the response into v.
// It handles the {"data": ...} envelope.
func (c *Client) do(ctx context.Context, req *http.Request, v any) (*Response, error) {
	respBody, response, err := c.execute(ctx, req)
	if err != nil {
		return response, err
	}

	if v != nil && len(respBody) > 0 {
		var env dataEnvelope
		if err := json.Unmarshal(respBody, &env); err != nil {
			return response, fmt.Errorf("posthook: failed to parse response: %w", err)
		}
		if len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, v); err != nil {
				return response, fmt.Errorf("posthook: failed to parse response data: %w", err)
			}
		}
	}

	return response, nil
}

// parseQuota extracts hook quota information from response headers.
func parseQuota(resp *http.Response, r *Response) {
	limit := resp.Header.Get("Posthook-HookQuota-Limit")
	if limit == "" {
		return
	}

	q := &QuotaInfo{}
	q.Limit, _ = strconv.Atoi(limit)
	q.Usage, _ = strconv.Atoi(resp.Header.Get("Posthook-HookQuota-Usage"))
	q.Remaining, _ = strconv.Atoi(resp.Header.Get("Posthook-HookQuota-Remaining"))
	if raw := resp.Header.Get("Posthook-HookQuota-Resets-At"); raw != "" {
		q.ResetsAt, _ = time.Parse(time.RFC3339, raw) // zero value on failure
	}
	r.Quota = q
}

// addQueryParams adds HookListParams as query parameters to the URL.
func addQueryParams(u *url.URL, params *HookListParams) {
	if params == nil {
		return
	}

	q := u.Query()
	if params.Status != "" {
		q.Set("status", params.Status)
	}
	if params.Limit != 0 {
		q.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.Offset != 0 {
		q.Set("offset", strconv.Itoa(params.Offset))
	}
	if !params.PostAtBefore.IsZero() {
		q.Set("postAtBefore", params.PostAtBefore.Format(time.RFC3339Nano))
	}
	if !params.PostAtAfter.IsZero() {
		q.Set("postAtAfter", params.PostAtAfter.Format(time.RFC3339Nano))
	}
	if !params.CreatedAtBefore.IsZero() {
		q.Set("createdAtBefore", params.CreatedAtBefore.Format(time.RFC3339Nano))
	}
	if !params.CreatedAtAfter.IsZero() {
		q.Set("createdAtAfter", params.CreatedAtAfter.Format(time.RFC3339Nano))
	}
	if params.SortBy != "" {
		q.Set("sortBy", params.SortBy)
	}
	if params.SortOrder != "" {
		q.Set("sortOrder", params.SortOrder)
	}
	u.RawQuery = q.Encode()
}

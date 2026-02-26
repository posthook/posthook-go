package posthook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClient creates a Client configured to use the given test server handler.
func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient("pk_test_key", WithBaseURL(server.URL))
	require.NoError(t, err)
	return client
}

// jsonResponse writes a JSON envelope response.
func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	env := map[string]any{"data": data}
	json.NewEncoder(w).Encode(env)
}

// jsonErrorResponse writes a JSON error response.
func jsonErrorResponse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	env := map[string]any{"error": message}
	json.NewEncoder(w).Encode(env)
}

func TestNewClient_RequiresAPIKey(t *testing.T) {
	t.Setenv("POSTHOOK_API_KEY", "")
	_, err := NewClient("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key is required")
}

func TestNewClient_EnvVarFallback(t *testing.T) {
	t.Setenv("POSTHOOK_API_KEY", "pk_env_key")
	client, err := NewClient("")
	require.NoError(t, err)
	assert.Equal(t, "pk_env_key", client.apiKey)
}

func TestNewClient_ExplicitKeyTakesPrecedence(t *testing.T) {
	t.Setenv("POSTHOOK_API_KEY", "pk_env_key")
	client, err := NewClient("pk_explicit")
	require.NoError(t, err)
	assert.Equal(t, "pk_explicit", client.apiKey)
}

func TestNewClient_Options(t *testing.T) {
	client, err := NewClient("pk_test",
		WithUserAgent("custom-agent/1.0"),
	)
	require.NoError(t, err)
	assert.Equal(t, "custom-agent/1.0", client.userAgent)
}

func TestNewClient_WithBaseURL(t *testing.T) {
	client, err := NewClient("pk_test", WithBaseURL("https://custom.api.com"))
	require.NoError(t, err)
	assert.Equal(t, "https://custom.api.com", client.baseURL.String())
}

func TestNewClient_InvalidBaseURL(t *testing.T) {
	_, err := NewClient("pk_test", WithBaseURL("://invalid"))
	require.Error(t, err)
}

func TestNewClient_WithSigningKey(t *testing.T) {
	client, err := NewClient("pk_test", WithSigningKey("ph_sk_test"))
	require.NoError(t, err)
	assert.Equal(t, "ph_sk_test", client.Signatures.signingKey)
}

func TestRequest_Headers(t *testing.T) {
	var capturedReq *http.Request
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		jsonResponse(w, http.StatusOK, map[string]string{"id": "test"})
	})

	req, err := client.newRequest(http.MethodPost, "/v1/hooks", map[string]string{"key": "val"})
	require.NoError(t, err)
	_, err = client.do(context.Background(), req, nil)
	require.NoError(t, err)

	assert.Equal(t, "pk_test_key", capturedReq.Header.Get("X-API-Key"))
	assert.Equal(t, defaultUserAgent, capturedReq.Header.Get("User-Agent"))
	assert.Equal(t, "application/json", capturedReq.Header.Get("Content-Type"))
}

func TestContextCancellation(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	req, err := client.newRequest(http.MethodGet, "/v1/test", nil)
	require.NoError(t, err)

	_, err = client.do(ctx, req, nil)
	require.Error(t, err)
}

func TestQuotaHeaderParsing(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Posthook-HookQuota-Limit", "10000")
		w.Header().Set("Posthook-HookQuota-Usage", "5000")
		w.Header().Set("Posthook-HookQuota-Remaining", "5000")
		w.Header().Set("Posthook-HookQuota-Resets-At", "2026-03-01T00:00:00Z")
		jsonResponse(w, http.StatusOK, map[string]string{"id": "test"})
	})

	req, err := client.newRequest(http.MethodGet, "/v1/test", nil)
	require.NoError(t, err)

	resp, err := client.do(context.Background(), req, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.Quota)

	assert.Equal(t, 10000, resp.Quota.Limit)
	assert.Equal(t, 5000, resp.Quota.Usage)
	assert.Equal(t, 5000, resp.Quota.Remaining)
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), resp.Quota.ResetsAt)
}

func TestNoQuotaHeaders(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]string{"id": "test"})
	})

	req, err := client.newRequest(http.MethodGet, "/v1/test", nil)
	require.NoError(t, err)

	resp, err := client.do(context.Background(), req, nil)
	require.NoError(t, err)
	assert.Nil(t, resp.Quota)
}

func TestErrorParsing(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonErrorResponse(w, http.StatusBadRequest, "path is required")
	})

	req, err := client.newRequest(http.MethodPost, "/v1/hooks", nil)
	require.NoError(t, err)

	_, err = client.do(context.Background(), req, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestAddQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		params   *HookListParams
		expected string
	}{
		{
			name:     "nil params",
			params:   nil,
			expected: "https://api.posthook.io/v1/hooks",
		},
		{
			name: "status only",
			params: &HookListParams{
				Status: StatusFailed,
			},
			expected: "https://api.posthook.io/v1/hooks?status=failed",
		},
		{
			name: "multiple params",
			params: &HookListParams{
				Status:    StatusFailed,
				Limit:     50,
				Offset:    100,
				SortBy:    SortByCreatedAt,
				SortOrder: SortOrderDesc,
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := url.Parse("https://api.posthook.io/v1/hooks")
			addQueryParams(u, tt.params)

			if tt.expected != "" {
				assert.Equal(t, tt.expected, u.String())
			} else {
				// For multiple params, just verify they're all present.
				q := u.Query()
				assert.Equal(t, "failed", q.Get("status"))
				assert.Equal(t, "50", q.Get("limit"))
				assert.Equal(t, "100", q.Get("offset"))
				assert.Equal(t, "createdAt", q.Get("sortBy"))
				assert.Equal(t, "DESC", q.Get("sortOrder"))
			}
		})
	}
}


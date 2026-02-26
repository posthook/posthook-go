package posthook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHooks_Schedule_PostIn(t *testing.T) {
	var capturedBody map[string]any
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/hooks", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)

		jsonResponse(w, http.StatusCreated, map[string]any{
			"id":     "hook-123",
			"path":   "/webhooks/test",
			"status": "pending",
			"postAt": "2026-02-22T15:05:00Z",
		})
	})

	hook, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:   "/webhooks/test",
		PostIn: "5m",
		Data:   map[string]any{"userId": "123"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hook-123", hook.ID)
	assert.Equal(t, "pending", hook.Status)
	assert.Equal(t, "/webhooks/test", capturedBody["path"])
	assert.Equal(t, "5m", capturedBody["postIn"])
}

func TestHooks_Schedule_PostAt(t *testing.T) {
	var capturedBody map[string]any
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)

		jsonResponse(w, http.StatusCreated, map[string]any{
			"id":     "hook-456",
			"status": "pending",
		})
	})

	hook, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:   "/webhooks/reminder",
		PostAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Data:   map[string]any{"userId": "123"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hook-456", hook.ID)

	// PostAt should be serialized to RFC3339.
	assert.Equal(t, "2026-03-01T12:00:00Z", capturedBody["postAt"])
}

func TestHooks_Schedule_PostAt_OmittedWhenZero(t *testing.T) {
	var capturedBody map[string]any
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)

		jsonResponse(w, http.StatusCreated, map[string]any{"id": "hook-z"})
	})

	_, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:   "/webhooks/test",
		PostIn: "5m",
		Data:   map[string]any{},
	})
	require.NoError(t, err)
	_, hasPostAt := capturedBody["postAt"]
	assert.False(t, hasPostAt, "zero PostAt should be omitted from JSON")
}

func TestHooks_Schedule_PostAtLocal(t *testing.T) {
	var capturedBody map[string]any
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)

		jsonResponse(w, http.StatusCreated, map[string]any{
			"id":     "hook-789",
			"status": "pending",
		})
	})

	_, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:        "/webhooks/local",
		PostAtLocal: "2026-03-01T09:00:00",
		Timezone:    "America/New_York",
		Data:        map[string]any{},
	})
	require.NoError(t, err)
	assert.Equal(t, "2026-03-01T09:00:00", capturedBody["postAtLocal"])
	assert.Equal(t, "America/New_York", capturedBody["timezone"])
}

func TestHooks_Schedule_RetryOverride(t *testing.T) {
	var capturedBody map[string]any
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)

		jsonResponse(w, http.StatusCreated, map[string]any{"id": "hook-retry"})
	})

	_, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:   "/webhooks/retry",
		PostIn: "1m",
		Data:   map[string]any{},
		RetryOverride: &HookRetryOverride{
			MinRetries:    5,
			DelaySecs:     10,
			Strategy:      "exponential",
			BackoffFactor: 2.0,
			MaxDelaySecs:  3600,
			Jitter:        Bool(true),
		},
	})
	require.NoError(t, err)

	override, ok := capturedBody["retryOverride"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(5), override["minRetries"])
	assert.Equal(t, "exponential", override["strategy"])
	assert.Equal(t, float64(2.0), override["backoffFactor"])
	assert.Equal(t, true, override["jitter"])
}

func TestHooks_Schedule_QuotaHeaders(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Posthook-HookQuota-Limit", "10000")
		w.Header().Set("Posthook-HookQuota-Usage", "1234")
		w.Header().Set("Posthook-HookQuota-Remaining", "8766")
		w.Header().Set("Posthook-HookQuota-Resets-At", "2026-03-01T00:00:00Z")
		jsonResponse(w, http.StatusCreated, map[string]any{"id": "hook-q"})
	})

	_, resp, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:   "/test",
		PostIn: "1m",
		Data:   map[string]any{},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Quota)
	assert.Equal(t, 10000, resp.Quota.Limit)
	assert.Equal(t, 1234, resp.Quota.Usage)
	assert.Equal(t, 8766, resp.Quota.Remaining)
	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), resp.Quota.ResetsAt)
}

func TestHooks_Get(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/hooks/hook-abc", r.URL.Path)

		jsonResponse(w, http.StatusOK, map[string]any{
			"id":                  "hook-abc",
			"path":                "/webhooks/test",
			"status":              "completed",
			"postDurationSeconds": 0.45,
			"attempts":            1,
		})
	})

	hook, _, err := client.Hooks.Get(context.Background(), "hook-abc")
	require.NoError(t, err)
	assert.Equal(t, "hook-abc", hook.ID)
	assert.Equal(t, "completed", hook.Status)
	assert.Equal(t, 0.45, hook.PostDurationSeconds)
	assert.Equal(t, 1, hook.Attempts)
}

func TestHooks_List(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/hooks", r.URL.Path)
		assert.Equal(t, "failed", r.URL.Query().Get("status"))
		assert.Equal(t, "50", r.URL.Query().Get("limit"))

		jsonResponse(w, http.StatusOK, []map[string]any{
			{"id": "h1", "status": "failed"},
			{"id": "h2", "status": "failed"},
		})
	})

	hooks, _, err := client.Hooks.List(context.Background(), &HookListParams{
		Status: StatusFailed,
		Limit:  50,
	})
	require.NoError(t, err)
	assert.Len(t, hooks, 2)
	assert.Equal(t, "h1", hooks[0].ID)
}

func TestHooks_List_NilParams(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.RawQuery)
		jsonResponse(w, http.StatusOK, []map[string]any{})
	})

	hooks, _, err := client.Hooks.List(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, hooks, 0)
}

func TestHooks_Delete_200(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/v1/hooks/hook-del", r.URL.Path)
		jsonResponse(w, http.StatusOK, nil)
	})

	resp, err := client.Hooks.Delete(context.Background(), "hook-del")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHooks_Delete_404_ReturnsNilError(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonErrorResponse(w, http.StatusNotFound, "hook not found")
	})

	_, err := client.Hooks.Delete(context.Background(), "hook-gone")
	assert.NoError(t, err)
}

func TestHooks_Delete_OtherErrorsPropagated(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonErrorResponse(w, http.StatusForbidden, "forbidden")
	})

	_, err := client.Hooks.Delete(context.Background(), "hook-id")
	require.Error(t, err)
	var forbidden *ForbiddenError
	assert.True(t, errors.As(err, &forbidden))
}

func TestBulk_Retry(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/hooks/bulk/retry", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Nil(t, body["projectID"])

		jsonResponse(w, http.StatusOK, map[string]any{"affected": 5})
	})

	result, _, err := client.Hooks.Bulk().Retry(context.Background(), &BulkActionByIDs{
		HookIDs: []string{"h1", "h2", "h3"},
	})
	require.NoError(t, err)
	assert.Equal(t, 5, result.Affected)
}

func TestBulk_RetryByFilter(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/hooks/bulk/retry", r.URL.Path)

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Nil(t, body["projectID"])
		assert.NotEmpty(t, body["startTime"])

		jsonResponse(w, http.StatusOK, map[string]any{"affected": 10})
	})

	result, _, err := client.Hooks.Bulk().RetryByFilter(context.Background(), &BulkActionByFilter{
		StartTime: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC),
		Limit:     100,
	})
	require.NoError(t, err)
	assert.Equal(t, 10, result.Affected)
}

func TestBulk_Replay(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/hooks/bulk/replay", r.URL.Path)
		jsonResponse(w, http.StatusOK, map[string]any{"affected": 3})
	})

	result, _, err := client.Hooks.Bulk().Replay(context.Background(), &BulkActionByIDs{
		HookIDs: []string{"h1"},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, result.Affected)
}

func TestBulk_ReplayByFilter(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/hooks/bulk/replay", r.URL.Path)
		jsonResponse(w, http.StatusOK, map[string]any{"affected": 7})
	})

	result, _, err := client.Hooks.Bulk().ReplayByFilter(context.Background(), &BulkActionByFilter{
		StartTime:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC),
		EndpointKey: "/webhooks/test",
	})
	require.NoError(t, err)
	assert.Equal(t, 7, result.Affected)
}

func TestBulk_Cancel(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/hooks/bulk/cancel", r.URL.Path)
		jsonResponse(w, http.StatusOK, map[string]any{"affected": 2})
	})

	result, _, err := client.Hooks.Bulk().Cancel(context.Background(), &BulkActionByIDs{
		HookIDs: []string{"h1", "h2"},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Affected)
}

func TestBulk_CancelByFilter(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/hooks/bulk/cancel", r.URL.Path)
		jsonResponse(w, http.StatusOK, map[string]any{"affected": 15})
	})

	result, _, err := client.Hooks.Bulk().CancelByFilter(context.Background(), &BulkActionByFilter{
		StartTime:  time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC),
		SequenceID: "seq-1",
		Limit:      50,
	})
	require.NoError(t, err)
	assert.Equal(t, 15, result.Affected)
}

func TestHooks_ListAll(t *testing.T) {
	callCount := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/hooks", r.URL.Path)
		assert.Equal(t, "postAt", r.URL.Query().Get("sortBy"))
		assert.Equal(t, "ASC", r.URL.Query().Get("sortOrder"))

		callCount++
		if callCount == 1 {
			assert.Empty(t, r.URL.Query().Get("postAtAfter"))
			jsonResponse(w, http.StatusOK, []map[string]any{
				{"id": "h1", "status": "failed", "postAt": "2026-02-01T10:00:00Z"},
				{"id": "h2", "status": "failed", "postAt": "2026-02-02T10:00:00Z"},
			})
		} else {
			assert.Equal(t, "2026-02-02T10:00:00Z", r.URL.Query().Get("postAtAfter"))
			jsonResponse(w, http.StatusOK, []map[string]any{
				{"id": "h3", "status": "failed", "postAt": "2026-02-03T10:00:00Z"},
			})
		}
	})

	var hooks []*Hook
	for hook, err := range client.Hooks.ListAll(context.Background(), &HookListAllParams{
		Status:   StatusFailed,
		PageSize: 2,
	}) {
		require.NoError(t, err)
		hooks = append(hooks, hook)
	}

	assert.Len(t, hooks, 3)
	assert.Equal(t, "h1", hooks[0].ID)
	assert.Equal(t, "h3", hooks[2].ID)
	assert.Equal(t, 2, callCount)
}

func TestHooks_ListAll_Empty(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, []map[string]any{})
	})

	var hooks []*Hook
	for hook, err := range client.Hooks.ListAll(context.Background(), nil) {
		require.NoError(t, err)
		hooks = append(hooks, hook)
	}

	assert.Len(t, hooks, 0)
}

func TestHooks_ListAll_Error(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonErrorResponse(w, http.StatusForbidden, "forbidden")
	})

	for _, err := range client.Hooks.ListAll(context.Background(), &HookListAllParams{
		Status: StatusFailed,
	}) {
		require.Error(t, err)
		var forbidden *ForbiddenError
		assert.True(t, errors.As(err, &forbidden))
		break
	}
}

func TestHooks_Get_EmptyID(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called")
	})

	_, _, err := client.Hooks.Get(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook id is required")
}

func TestHooks_Delete_EmptyID(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called")
	})

	_, err := client.Hooks.Delete(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook id is required")
}

func TestHooks_Schedule_NoSchedulingMode(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called")
	})

	_, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path: "/webhooks/test",
		Data: map[string]any{"key": "value"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one of PostAt, PostAtLocal, or PostIn must be set")
}

func TestHooks_Schedule_MultipleSchedulingModes(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called")
	})

	_, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:   "/webhooks/test",
		PostIn: "5m",
		PostAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Data:   map[string]any{"key": "value"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only one of PostAt, PostAtLocal, or PostIn may be set (got 2)")
}

func TestHooks_Schedule_AllThreeSchedulingModes(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called")
	})

	_, _, err := client.Hooks.Schedule(context.Background(), &HookScheduleParams{
		Path:        "/webhooks/test",
		PostIn:      "5m",
		PostAt:      time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		PostAtLocal: "2026-03-01T09:00:00",
		Data:        map[string]any{"key": "value"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only one of PostAt, PostAtLocal, or PostIn may be set (got 3)")
}

package posthook

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"time"
)

// HooksService handles communication with the hook-related endpoints of the
// Posthook API.
type HooksService service

// Schedule creates a new scheduled hook. Exactly one of PostAt, PostAtLocal
// (with Timezone), or PostIn must be set on params.
func (s *HooksService) Schedule(ctx context.Context, params *HookScheduleParams) (*Hook, *Response, error) {
	modes := 0
	if !params.PostAt.IsZero() {
		modes++
	}
	if params.PostAtLocal != "" {
		modes++
	}
	if params.PostIn != "" {
		modes++
	}
	if modes == 0 {
		return nil, nil, fmt.Errorf("posthook: exactly one of PostAt, PostAtLocal, or PostIn must be set")
	}
	if modes > 1 {
		return nil, nil, fmt.Errorf("posthook: only one of PostAt, PostAtLocal, or PostIn may be set (got %d)", modes)
	}

	req, err := s.client.newRequest(http.MethodPost, "/v1/hooks", params)
	if err != nil {
		return nil, nil, err
	}

	hook := new(Hook)
	resp, err := s.client.do(ctx, req, hook)
	if err != nil {
		return nil, resp, err
	}

	return hook, resp, nil
}

// Get retrieves a single hook by its ID.
func (s *HooksService) Get(ctx context.Context, id string) (*Hook, *Response, error) {
	if id == "" {
		return nil, nil, fmt.Errorf("posthook: hook id is required")
	}

	req, err := s.client.newRequest(http.MethodGet, fmt.Sprintf("/v1/hooks/%s", url.PathEscape(id)), nil)
	if err != nil {
		return nil, nil, err
	}

	hook := new(Hook)
	resp, err := s.client.do(ctx, req, hook)
	if err != nil {
		return nil, resp, err
	}

	return hook, resp, nil
}

// List retrieves a paginated list of hooks. Use Limit and Offset on
// HookListParams for pagination. When the returned slice is shorter than
// the requested Limit, you have reached the last page.
func (s *HooksService) List(ctx context.Context, params *HookListParams) ([]*Hook, *Response, error) {
	req, err := s.client.newRequest(http.MethodGet, "/v1/hooks", nil)
	if err != nil {
		return nil, nil, err
	}

	addQueryParams(req.URL, params)

	var hooks []*Hook
	resp, err := s.client.do(ctx, req, &hooks)
	if err != nil {
		return nil, resp, err
	}

	return hooks, resp, nil
}

// ListAll returns an iterator that yields every matching hook across all pages.
// It uses cursor-based pagination via postAt ordering for consistency.
//
// Usage:
//
//	for hook, err := range client.Hooks.ListAll(ctx, &posthook.HookListAllParams{Status: posthook.StatusFailed}) {
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    fmt.Println(hook.ID)
//	}
func (s *HooksService) ListAll(ctx context.Context, params *HookListAllParams) iter.Seq2[*Hook, error] {
	return func(yield func(*Hook, error) bool) {
		pageSize := 100
		if params != nil && params.PageSize > 0 {
			pageSize = params.PageSize
		}

		var status string
		var cursor time.Time
		if params != nil {
			status = params.Status
			cursor = params.PostAtAfter
		}

		for {
			listParams := &HookListParams{
				Status:      status,
				Limit:       pageSize,
				SortBy:      SortByPostAt,
				SortOrder:   SortOrderAsc,
				PostAtAfter: cursor,
			}

			hooks, _, err := s.List(ctx, listParams)
			if err != nil {
				yield(nil, err)
				return
			}

			for _, hook := range hooks {
				if !yield(hook, nil) {
					return
				}
			}

			if len(hooks) < pageSize {
				return
			}
			cursor = hooks[len(hooks)-1].PostAt
		}
	}
}

// Delete removes a hook. To cancel a pending hook, delete it before
// delivery. Returns nil error on both 200 (deleted) and 404 (already deleted).
func (s *HooksService) Delete(ctx context.Context, id string) (*Response, error) {
	if id == "" {
		return nil, fmt.Errorf("posthook: hook id is required")
	}

	req, err := s.client.newRequest(http.MethodDelete, fmt.Sprintf("/v1/hooks/%s", url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.do(ctx, req, nil)
	if err != nil {
		// Swallow 404 — the hook was already deleted.
		var notFound *NotFoundError
		if errors.As(err, &notFound) {
			return resp, nil
		}
		return resp, err
	}

	return resp, nil
}

// Bulk returns a BulkActions handle for performing bulk operations on hooks.
func (s *HooksService) Bulk() *BulkActions {
	return &BulkActions{client: s.client}
}

// BulkActions provides methods for performing bulk operations on hooks.
type BulkActions struct {
	client *Client
}

// Retry re-attempts delivery for failed hooks specified by their IDs.
func (b *BulkActions) Retry(ctx context.Context, params *BulkActionByIDs) (*BulkActionResult, *Response, error) {
	return b.doBulk(ctx, "/v1/hooks/bulk/retry", params)
}

// RetryByFilter re-attempts delivery for failed hooks matching a time range filter.
func (b *BulkActions) RetryByFilter(ctx context.Context, params *BulkActionByFilter) (*BulkActionResult, *Response, error) {
	return b.doBulk(ctx, "/v1/hooks/bulk/retry", params)
}

// Replay re-delivers completed hooks specified by their IDs.
func (b *BulkActions) Replay(ctx context.Context, params *BulkActionByIDs) (*BulkActionResult, *Response, error) {
	return b.doBulk(ctx, "/v1/hooks/bulk/replay", params)
}

// ReplayByFilter re-delivers completed hooks matching a time range filter.
func (b *BulkActions) ReplayByFilter(ctx context.Context, params *BulkActionByFilter) (*BulkActionResult, *Response, error) {
	return b.doBulk(ctx, "/v1/hooks/bulk/replay", params)
}

// Cancel cancels pending hooks specified by their IDs.
func (b *BulkActions) Cancel(ctx context.Context, params *BulkActionByIDs) (*BulkActionResult, *Response, error) {
	return b.doBulk(ctx, "/v1/hooks/bulk/cancel", params)
}

// CancelByFilter cancels pending hooks matching a time range filter.
func (b *BulkActions) CancelByFilter(ctx context.Context, params *BulkActionByFilter) (*BulkActionResult, *Response, error) {
	return b.doBulk(ctx, "/v1/hooks/bulk/cancel", params)
}

func (b *BulkActions) doBulk(ctx context.Context, path string, body any) (*BulkActionResult, *Response, error) {
	req, err := b.client.newRequest(http.MethodPost, path, body)
	if err != nil {
		return nil, nil, err
	}

	result := new(BulkActionResult)
	resp, err := b.client.do(ctx, req, result)
	if err != nil {
		return nil, resp, err
	}

	return result, resp, nil
}

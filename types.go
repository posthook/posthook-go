package posthook

import (
	"encoding/json"
	"time"
)

// Bool returns a pointer to the given bool value. This is a convenience helper
// for setting optional *bool fields like HookRetryOverride.Jitter.
func Bool(v bool) *bool { return &v }

// Hook status values for use with HookListParams.Status.
const (
	StatusPending   = "pending"   // Hooks awaiting delivery (includes dispatching and retry).
	StatusRetry     = "retry"     // Hooks waiting to be retried after a failed attempt.
	StatusCompleted = "completed" // Hooks that were delivered successfully.
	StatusFailed    = "failed"    // Hooks that exhausted all retry attempts.
)

// Sort field values for use with HookListParams.SortBy.
const (
	SortByPostAt    = "postAt"
	SortByCreatedAt = "createdAt"
)

// Sort order values for use with HookListParams.SortOrder.
const (
	SortOrderAsc  = "ASC"
	SortOrderDesc = "DESC"
)

// Retry strategy values for use with HookRetryOverride.Strategy.
const (
	StrategyFixed       = "fixed"
	StrategyExponential = "exponential"
)

// Hook represents a scheduled webhook.
type Hook struct {
	ID                  string             `json:"id"`
	Path                string             `json:"path"`
	Domain              *string            `json:"domain,omitempty"`
	Data                json.RawMessage    `json:"data"`
	PostAt              time.Time          `json:"postAt"`
	Status              string             `json:"status"`
	PostDurationSeconds float64            `json:"postDurationSeconds"`
	Attempts            int                `json:"attempts,omitempty"`
	FailureError        string             `json:"failureError,omitempty"`
	SequenceData        *HookSequenceData  `json:"sequenceData,omitempty"`
	RetryOverride       *HookRetryOverride `json:"retryOverride,omitempty"`
	CreatedAt           time.Time          `json:"createdAt"`
	UpdatedAt           time.Time          `json:"updatedAt"`
}

// HookSequenceData contains sequence context for a hook that is part of a sequence.
type HookSequenceData struct {
	SequenceID        string `json:"sequenceID"`
	StepName          string `json:"stepName"`
	SequenceLastRunAt string `json:"sequenceLastRunAt"`
}

// HookRetryOverride configures per-hook retry behavior.
type HookRetryOverride struct {
	MinRetries    int     `json:"minRetries"`
	DelaySecs     int     `json:"delaySecs"`
	Strategy      string  `json:"strategy"`
	BackoffFactor float64 `json:"backoffFactor,omitempty"`
	MaxDelaySecs  int     `json:"maxDelaySecs,omitempty"`
	Jitter        *bool   `json:"jitter,omitempty"`
}

// BulkActionResult contains the result of a bulk action.
type BulkActionResult struct {
	Affected int `json:"affected"`
}

// QuotaInfo contains hook quota information parsed from response headers.
type QuotaInfo struct {
	Limit     int       `json:"limit"`
	Usage     int       `json:"usage"`
	Remaining int       `json:"remaining"`
	ResetsAt  time.Time `json:"resetsAt"`
}

// deliveryPayload is the JSON body that Posthook POSTs to your endpoint.
type deliveryPayload struct {
	ID        string          `json:"id"`
	Path      string          `json:"path"`
	PostAt    string          `json:"postAt"`
	PostedAt  string          `json:"postedAt"`
	Data      json.RawMessage `json:"data"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

// CallbackResult is the result of an ack or nack callback.
// Both Ack() and Nack() return this for all expected outcomes, including race
// conditions where the hook already resolved. Check Applied to determine
// whether your callback changed the hook's state.
type CallbackResult struct {
	// Applied is true when the callback changed the hook's state.
	Applied bool `json:"applied"`
	// Status is the hook's current status (e.g. "completed", "nacked", "not_found", "conflict").
	Status string `json:"status"`
}

// Delivery is the parsed result of a verified webhook delivery. It contains
// the hook metadata extracted from headers and the parsed delivery body.
type Delivery struct {
	HookID    string          `json:"hookId"`
	Timestamp int64           `json:"timestamp"`
	Path      string          `json:"path"`
	Data      json.RawMessage `json:"data"`
	PostAt    time.Time       `json:"postAt"`
	PostedAt  time.Time       `json:"postedAt"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`

	// AckURL is the callback URL for acknowledging async processing.
	// Present when both Posthook-Ack-URL and Posthook-Nack-URL headers exist.
	AckURL string `json:"ackUrl,omitempty"`

	// NackURL is the callback URL for negative acknowledgement.
	// Present when both Posthook-Ack-URL and Posthook-Nack-URL headers exist.
	NackURL string `json:"nackUrl,omitempty"`

	// Body contains the raw HTTP request body bytes. It is not included
	// in JSON serialization and is provided for caller convenience.
	Body []byte `json:"-"`
}

// HookScheduleParams contains the parameters for scheduling a new hook.
// Exactly one of PostAt, PostAtLocal (with Timezone), or PostIn must be provided.
type HookScheduleParams struct {
	// Path is the endpoint path that will be appended to your project's domain.
	Path string

	// Data is the JSON payload to deliver. Accepts any JSON-serializable value.
	Data any

	// PostAt schedules delivery at an absolute UTC time.
	PostAt time.Time

	// PostAtLocal schedules delivery at a local time. Must be used with Timezone.
	PostAtLocal string

	// Timezone is the IANA timezone for PostAtLocal (e.g., "America/New_York").
	Timezone string

	// PostIn schedules delivery after a relative delay (e.g., "5m", "2h").
	PostIn string

	// RetryOverride configures per-hook retry behavior.
	RetryOverride *HookRetryOverride
}

// MarshalJSON implements custom JSON marshaling that omits zero-value optional
// fields. This is needed because encoding/json's omitempty does not treat
// time.Time zero values as empty.
func (p HookScheduleParams) MarshalJSON() ([]byte, error) {
	type wire struct {
		Path          string             `json:"path"`
		Data          any                `json:"data"`
		PostAt        *time.Time         `json:"postAt,omitempty"`
		PostAtLocal   string             `json:"postAtLocal,omitempty"`
		Timezone      string             `json:"timezone,omitempty"`
		PostIn        string             `json:"postIn,omitempty"`
		RetryOverride *HookRetryOverride `json:"retryOverride,omitempty"`
	}
	out := wire{
		Path:          p.Path,
		Data:          p.Data,
		PostAtLocal:   p.PostAtLocal,
		Timezone:      p.Timezone,
		PostIn:        p.PostIn,
		RetryOverride: p.RetryOverride,
	}
	if !p.PostAt.IsZero() {
		out.PostAt = &p.PostAt
	}
	return json.Marshal(out)
}

// HookListParams contains the query parameters for listing hooks.
// All fields are optional; zero values are ignored.
type HookListParams struct {
	Status          string
	Limit           int
	Offset          int
	PostAtBefore    time.Time
	PostAtAfter     time.Time
	CreatedAtBefore time.Time
	CreatedAtAfter  time.Time
	SortBy          string
	SortOrder       string
}

// HookListAllParams contains the parameters for auto-paginating hook listing.
// Uses cursor-based pagination via postAt ordering.
type HookListAllParams struct {
	// Status filters hooks by status (e.g., StatusFailed).
	Status string

	// PostAtAfter is the start cursor: only return hooks scheduled after this time (exclusive).
	PostAtAfter time.Time

	// PageSize is the number of hooks to fetch per page (default 100, max 1000).
	PageSize int
}

// BulkActionByIDs specifies hooks to act on by their individual IDs.
type BulkActionByIDs struct {
	HookIDs []string `json:"hookIDs"`
}

// BulkActionByFilter specifies hooks to act on using a time range filter.
type BulkActionByFilter struct {
	StartTime   time.Time `json:"startTime"`
	EndTime     time.Time `json:"endTime"`
	EndpointKey string    `json:"endpointKey,omitempty"`
	SequenceID  string    `json:"sequenceID,omitempty"`
	Limit       int       `json:"limit,omitempty"`
}

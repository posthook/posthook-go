# posthook-go

The official Go client library for the [Posthook](https://posthook.io) API.

## Installation

```bash
go get github.com/posthook/posthook-go
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    posthook "github.com/posthook/posthook-go"
)

func main() {
    client, err := posthook.NewClient("pk_...")
    if err != nil {
        log.Fatal(err)
    }

    hook, resp, err := client.Hooks.Schedule(context.Background(), &posthook.HookScheduleParams{
        Path:   "/webhooks/user-created",
        PostIn: "5m",
        Data:   map[string]any{"userId": "123", "event": "user.created"},
    })
    if err != nil {
        log.Printf("Failed to schedule hook: %v", err)
        return
    }

    fmt.Printf("Scheduled hook %s (status: %s)\n", hook.ID, hook.Status)
    if resp.Quota != nil {
        fmt.Printf("Quota: %d/%d remaining\n", resp.Quota.Remaining, resp.Quota.Limit)
    }
}
```

## How It Works

Your Posthook project has a **domain** configured in the dashboard (e.g., `webhook.example.com`). When you schedule a hook, you specify a **Path** (e.g., `/webhooks/user-created`). At the scheduled time, Posthook delivers the hook by POSTing to the full URL (`https://webhook.example.com/webhooks/user-created`) with your data payload and signature headers.

## Authentication

You can find your API key under **Project Settings** in the [Posthook dashboard](https://posthook.io). Pass it directly to `NewClient`, or set the `POSTHOOK_API_KEY` environment variable:

```go
// Explicit API key
client, err := posthook.NewClient("pk_...")

// From environment variable
client, err := posthook.NewClient("")  // reads POSTHOOK_API_KEY
```

For webhook signature verification, also provide a signing key:

```go
client, err := posthook.NewClient("pk_...", posthook.WithSigningKey("ph_sk_..."))
```

## Scheduling Hooks

Three scheduling modes are available:

### Absolute UTC time (`PostAt`)

Schedule at an exact UTC time:

```go
hook, _, err := client.Hooks.Schedule(ctx, &posthook.HookScheduleParams{
    Path:   "/webhooks/reminder",
    PostAt: time.Now().Add(24 * time.Hour),
    Data:   map[string]any{"userId": "123"},
})
```

### Local time with timezone (`PostAtLocal` + `Timezone`)

Schedule at a local time that respects DST:

```go
hook, _, err := client.Hooks.Schedule(ctx, &posthook.HookScheduleParams{
    Path:        "/webhooks/daily-digest",
    PostAtLocal: "2026-03-01T09:00:00",
    Timezone:    "America/New_York",
    Data:        map[string]any{"userId": "123"},
})
```

### Relative delay (`PostIn`)

Schedule after a relative delay:

```go
hook, _, err := client.Hooks.Schedule(ctx, &posthook.HookScheduleParams{
    Path:   "/webhooks/followup",
    PostIn: "30m",
    Data:   map[string]any{"userId": "123"},
})
```

### Custom retry configuration

Override the default retry behavior for a specific hook:

```go
hook, _, err := client.Hooks.Schedule(ctx, &posthook.HookScheduleParams{
    Path:   "/webhooks/critical",
    PostIn: "1m",
    Data:   map[string]any{"orderId": "456"},
    RetryOverride: &posthook.HookRetryOverride{
        MinRetries:    10,
        DelaySecs:     15,
        Strategy:      "exponential",
        BackoffFactor: 2.0,
        MaxDelaySecs:  3600,
        Jitter:        posthook.Bool(true),
    },
})
```

## Managing Hooks

### Get a hook

```go
hook, _, err := client.Hooks.Get(ctx, "hook-uuid")
```

### List hooks

```go
hooks, _, err := client.Hooks.List(ctx, &posthook.HookListParams{
    Status:    posthook.StatusFailed,
    Limit:     50,
    SortBy:    posthook.SortByCreatedAt,
    SortOrder: posthook.SortOrderDesc,
})
fmt.Printf("Found %d hooks\n", len(hooks))
```

### Cursor-based pagination

Use `PostAtAfter` as a cursor. After each page, advance it to the last hook's `PostAt`:

```go
limit := 100
var cursor time.Time
for {
    hooks, _, err := client.Hooks.List(ctx, &posthook.HookListParams{
        Status:      posthook.StatusFailed,
        Limit:       limit,
        PostAtAfter: cursor,
    })
    if err != nil {
        log.Fatal(err)
    }

    for _, hook := range hooks {
        fmt.Println(hook.ID, hook.FailureError)
    }

    if len(hooks) < limit {
        break // last page
    }
    cursor = hooks[len(hooks)-1].PostAt
}
```

Or use `ListAll` to auto-paginate:

```go
iter := client.Hooks.ListAll(ctx, &posthook.HookListAllParams{
    Status: posthook.StatusFailed,
})
for hook, err := range iter {
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(hook.ID, hook.FailureError)
}
```

### Delete a hook

Returns `nil` error on both 200 (deleted) and 404 (already delivered or gone):

```go
_, err := client.Hooks.Delete(ctx, "hook-uuid")
```

## Bulk Actions

Three bulk operations are available, each supporting by-IDs or by-filter:

- **Retry** — Re-attempts delivery for failed hooks
- **Replay** — Re-delivers completed hooks (useful for reprocessing)
- **Cancel** — Cancels pending hooks before delivery

### By IDs

```go
result, _, err := client.Hooks.Bulk().Retry(ctx, &posthook.BulkActionByIDs{
    HookIDs: []string{"id-1", "id-2", "id-3"},
})
fmt.Printf("Retried %d hooks\n", result.Affected)
```

### By filter

```go
result, _, err := client.Hooks.Bulk().CancelByFilter(ctx, &posthook.BulkActionByFilter{
    StartTime:   time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
    EndTime:     time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC),
    EndpointKey: "/webhooks/deprecated",
    Limit:       500,
})
fmt.Printf("Cancelled %d hooks\n", result.Affected)
```

## Verifying Webhook Signatures

When Posthook delivers a hook to your endpoint, it includes signature headers for verification. Use `ParseDelivery` to verify and parse the delivery:

```go
client, _ := posthook.NewClient("pk_...", posthook.WithSigningKey("ph_sk_..."))

func handleWebhook(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)

    delivery, err := client.Signatures.ParseDelivery(body, r.Header)
    if err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }

    fmt.Println(delivery.HookID)  // from Posthook-Id header
    fmt.Println(delivery.Path)    // "/webhooks/user-created"
    fmt.Println(delivery.PostAt)  // when it was scheduled
    fmt.Println(delivery.PostedAt) // when it was delivered

    // Unmarshal your custom data
    var event struct {
        UserID string `json:"userId"`
        Event  string `json:"event"`
    }
    json.Unmarshal(delivery.Data, &event)

    w.WriteHeader(http.StatusOK)
}
```

### Custom tolerance

By default, signatures older than 5 minutes are rejected:

```go
delivery, err := client.Signatures.ParseDelivery(body, r.Header,
    posthook.WithTolerance(10 * time.Minute),
)
```

## Async Hooks

When [async hooks](https://posthook.io/docs/essentials/async-hooks) are enabled, `ParseDelivery` populates `AckURL` and `NackURL` on the delivery. Return 202 from your handler and call back when processing completes.

```go
func handleWebhook(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)

    delivery, err := client.Signatures.ParseDelivery(body, r.Header)
    if err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }

    w.WriteHeader(http.StatusAccepted)
    go func() {
        if err := processVideo(delivery.Data); err != nil {
            delivery.Nack(context.Background(), map[string]any{"error": err.Error()})
            return
        }
        delivery.Ack(context.Background(), nil)
    }()
}
```

Both `Ack()` and `Nack()` return a `*CallbackResult`:

```go
result, err := delivery.Ack(ctx, nil)
fmt.Println(result.Applied) // true if state changed, false if already resolved
fmt.Println(result.Status)  // "completed", "not_found", "conflict", etc.
```

`Ack()` and `Nack()` return a result (not an error) for `200`, `404`, and `409` responses. They return a `*CallbackError` for `401` (invalid token) and `410` (expired).

If processing happens in a separate worker, use the raw callback URLs instead:

```go
// Pass URLs through your queue
queue.Add("transcode", map[string]string{
    "videoId": videoId,
    "ackUrl":  delivery.AckURL,
    "nackUrl": delivery.NackURL,
})
```

## Error Handling

All API errors are typed, enabling precise error handling with `errors.As()`:

```go
hook, _, err := client.Hooks.Get(ctx, "hook-id")
if err != nil {
    var authErr *posthook.AuthenticationError
    var notFound *posthook.NotFoundError
    var rateLimit *posthook.RateLimitError

    switch {
    case errors.As(err, &authErr):
        log.Fatal("Invalid API key")
    case errors.As(err, &notFound):
        log.Println("Hook not found")
    case errors.As(err, &rateLimit):
        log.Println("Rate limited, retry later")
    default:
        log.Printf("Unexpected error: %v", err)
    }
}
```

Available error types: `BadRequestError` (400), `AuthenticationError` (401), `ForbiddenError` (403), `NotFoundError` (404), `PayloadTooLargeError` (413), `RateLimitError` (429), `InternalServerError` (5xx), `ConnectionError` (network), `SignatureVerificationError` (signature).

## Configuration

```go
client, err := posthook.NewClient("pk_...",
    posthook.WithBaseURL("https://api.staging.posthook.io"),
    posthook.WithHTTPClient(&http.Client{Timeout: 60 * time.Second}),
    posthook.WithUserAgent("my-app/1.0"),
    posthook.WithSigningKey("ph_sk_..."),
)
```

| Option | Description | Default |
|--------|-------------|---------|
| `WithBaseURL` | Custom API base URL | `https://api.posthook.io` |
| `WithHTTPClient` | Custom `*http.Client` | 30s timeout |
| `WithUserAgent` | Custom User-Agent header | `posthook-go/1.0.0` |
| `WithSigningKey` | Signing key for signature verification | — |

## Quota Info

Every response includes quota information when available:

```go
hook, resp, err := client.Hooks.Schedule(ctx, params)
if resp.Quota != nil {
    fmt.Printf("Limit: %d\n", resp.Quota.Limit)
    fmt.Printf("Usage: %d\n", resp.Quota.Usage)
    fmt.Printf("Remaining: %d\n", resp.Quota.Remaining)
    fmt.Printf("Resets at: %s\n", resp.Quota.ResetsAt.Format(time.RFC3339))
}
```

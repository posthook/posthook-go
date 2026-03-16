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

To cancel a pending hook, delete it before delivery. Idempotent — returns `nil` error on both 200 (deleted) and 404 (already deleted):

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

When [async hooks](https://docs.posthook.io/essentials/async-hooks) are enabled, `ParseDelivery` populates `AckURL` and `NackURL` on the delivery. Return 202 from your handler and call back when processing completes.

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

## WebSocket Listener

Receive hooks in real time over a persistent WebSocket connection instead of
running an HTTP server. Enable WebSocket delivery in your project settings first.

### Callback style (`Listen`)

Pass a handler function. The SDK manages the connection, heartbeat, and
reconnection automatically.

```go
client, _ := posthook.NewClient("pk_...")

listener, err := client.Hooks.Listen(ctx, func(ctx context.Context, d *posthook.Delivery) posthook.Result {
    fmt.Println(d.HookID, d.Data)
    fmt.Printf("Attempt %d/%d\n", d.WS.Attempt, d.WS.MaxAttempts)

    return posthook.Ack()
},
    posthook.WithMaxConcurrency(5),
    posthook.OnConnected(func(info posthook.ConnectionInfo) {
        fmt.Println("Connected:", info.ProjectName)
    }),
    posthook.OnDisconnected(func(err error) {
        fmt.Println("Disconnected:", err)
    }),
)
if err != nil {
    log.Fatal(err)
}

// Block until the listener is closed
listener.Wait()
```

**Result types:**

| Factory | Effect |
|---------|--------|
| `posthook.Ack()` | Processing complete — hook is marked as delivered immediately |
| `posthook.Nack(err)` | Reject — triggers retry according to project settings |
| `posthook.Accept(timeoutSecs)` | Async — you have `timeoutSecs` to call back via HTTP (see below) |

If your handler panics, the SDK automatically sends a nack with the panic message.

### Async processing with `Accept`

Use `Accept` when your handler needs more time than the 10-second ack window.
After returning `Accept`, POST to the callback URLs on the delivery to report
the outcome:

```go
listener, _ := client.Hooks.Listen(ctx, func(ctx context.Context, d *posthook.Delivery) posthook.Result {
    // Kick off background work, save the callback URLs
    queue.Add("process", map[string]string{
        "ackUrl":  d.AckURL,
        "nackUrl": d.NackURL,
    })
    return posthook.Accept(300) // 5 minutes to call back
})

// Later, in the background worker:
http.Post(job.AckURL, "application/json", nil)
// or on failure:
http.Post(job.NackURL, "application/json", strings.NewReader(`{"error":"failed"}`))
```

If neither URL is called before the deadline, the hook is retried.

### Iterator style (`Stream`)

For more control, use `Stream()` which returns a channel of deliveries:

```go
stream, err := client.Hooks.Stream(ctx,
    posthook.OnConnected(func(info posthook.ConnectionInfo) {
        fmt.Println("Connected:", info.ProjectName)
    }),
)
if err != nil {
    log.Fatal(err)
}

for delivery := range stream.Deliveries() {
    fmt.Println(delivery.HookID, delivery.Data)
    stream.Ack(delivery.HookID)
    // or: stream.Accept(delivery.HookID, 300)
    // or: stream.Nack(delivery.HookID, errors.New("bad data"))
}
```

### HTTP fallback

If your project has a domain configured, hooks are delivered via HTTP when no
WebSocket listener is connected. You can run both an HTTP endpoint and a
WebSocket listener — the server uses WebSocket when available and falls back to
HTTP automatically. Since both paths use the same `Result` type, you can share
your handler logic:

```go
func processHook(ctx context.Context, d *posthook.Delivery) posthook.Result {
    processOrder(d.Data)
    return posthook.Ack()
}

// HTTP delivery (net/http endpoint)
http.HandleFunc("/webhooks/order", func(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    delivery, err := client.Signatures.ParseDelivery(body, r.Header)
    if err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }
    result := processHook(r.Context(), delivery)
    // ... map result to HTTP response
})

// WebSocket delivery (runs alongside)
listener, _ := client.Hooks.Listen(ctx, processHook)
```

### Connection lifecycle

- **Reconnection:** On disconnect the SDK reconnects with exponential backoff
  (`min(1s * 2^attempts, 30s)`), up to 10 attempts.
- **Heartbeat:** If no server activity is detected for 45 seconds the
  connection is considered stale and force-closed for reconnection.
- **Auth errors:** Close codes `4001` and `4003` abort immediately without
  reconnecting.

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
| `WithUserAgent` | Custom User-Agent header | `posthook-go/{version}` |
| `WithSigningKey` | Signing key for signature verification | — |

## Resources

- [Documentation](https://docs.posthook.io) — guides, concepts, and patterns
- [API Reference](https://docs.posthook.io/api-reference/introduction) — endpoint specs and examples
- [Quickstart](https://docs.posthook.io/quickstart) — get started in under 2 minutes
- [Pricing](https://posthook.io/pricing) — free tier included
- [Status](https://status.posthook.io) — uptime and incident history

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

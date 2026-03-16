// Package posthook provides a Go client library for the Posthook API.
//
// Posthook is a webhook scheduling and delivery platform. This SDK allows you
// to schedule, manage, and verify webhook deliveries from your Go applications.
//
// # Usage
//
//	client, err := posthook.NewClient("pk_...")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Schedule a hook with a relative delay
//	hook, resp, err := client.Hooks.Schedule(ctx, &posthook.HookScheduleParams{
//	    Path:   "/webhooks/user-created",
//	    PostIn: "5m",
//	    Data:   map[string]any{"userId": "123"},
//	})
//
// # Authentication
//
// Pass your API key directly to NewClient, or set the POSTHOOK_API_KEY
// environment variable and pass an empty string:
//
//	client, err := posthook.NewClient("")  // uses POSTHOOK_API_KEY
//
// # Verifying Webhook Signatures
//
// When receiving deliveries, use the Signatures service to verify authenticity:
//
//	client, _ := posthook.NewClient("pk_...", posthook.WithSigningKey("ph_sk_..."))
//	delivery, err := client.Signatures.ParseDelivery(body, r.Header)
//
// For full documentation, see https://docs.posthook.io
package posthook

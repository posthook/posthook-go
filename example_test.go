package posthook_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	posthook "github.com/posthook/posthook-go"
)

func ExampleNewClient() {
	client, err := posthook.NewClient("pk_...")
	if err != nil {
		panic(err)
	}

	_ = client // use client to schedule hooks, verify signatures, etc.
}

func ExampleHooksService_Schedule() {
	client, err := posthook.NewClient("pk_...")
	if err != nil {
		panic(err)
	}

	hook, _, err := client.Hooks.Schedule(context.Background(), &posthook.HookScheduleParams{
		Path:   "/webhooks/user-created",
		PostIn: "5m",
		Data:   map[string]any{"userId": "123"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(hook.ID)
}

func ExampleHooksService_Schedule_postAt() {
	client, err := posthook.NewClient("pk_...")
	if err != nil {
		panic(err)
	}

	hook, _, err := client.Hooks.Schedule(context.Background(), &posthook.HookScheduleParams{
		Path:   "/webhooks/reminder",
		PostAt: time.Now().Add(24 * time.Hour),
		Data:   map[string]any{"userId": "123"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(hook.ID)
}

func ExampleSignaturesService_ParseDelivery() {
	client, err := posthook.NewClient("pk_...", posthook.WithSigningKey("ph_sk_..."))
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/webhooks/test", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		delivery, err := client.Signatures.ParseDelivery(body, r.Header)
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		fmt.Println(delivery.HookID, delivery.Path)
		w.WriteHeader(http.StatusOK)
	})
}

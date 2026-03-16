package posthook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testWSServer creates a test HTTP server with a ticket endpoint and a
// WebSocket endpoint. The returned channel receives each upgraded WebSocket
// connection. The optional authError parameter, if true, causes the ticket
// endpoint to return 401.
func testWSServer(t *testing.T, authError bool) (*httptest.Server, chan *websocket.Conn) {
	t.Helper()
	conns := make(chan *websocket.Conn, 8)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/ws/ticket", func(w http.ResponseWriter, r *http.Request) {
		if authError {
			jsonErrorResponse(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Build the WS URL from the request host.
		wsURL := "ws://" + r.Host + "/ws"
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"ticket":    "test-ticket",
				"url":       wsURL,
				"expiresAt": time.Now().Add(time.Minute).Format(time.RFC3339),
			},
		})
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Send connected message.
		conn.WriteJSON(map[string]any{
			"type":         "connected",
			"connectionId": "conn_test",
			"projectId":    "proj_test",
			"projectName":  "Test Project",
			"serverTime":   time.Now().Format(time.RFC3339),
		})
		conns <- conn
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, conns
}

// testListenClient creates a Client configured to use the test server.
func testListenClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := NewClient("pk_test_key", WithBaseURL(serverURL))
	require.NoError(t, err)
	return client
}

// sendHookMessage sends a hook message to the WebSocket connection.
func sendHookMessage(t *testing.T, conn *websocket.Conn, hookID string) {
	t.Helper()
	conn.WriteJSON(map[string]any{
		"type":        "hook",
		"id":          hookID,
		"path":        "/webhooks/test",
		"data":        map[string]any{"key": "value"},
		"postAt":      "2026-03-01T12:00:00Z",
		"postedAt":    "2026-03-01T12:00:01Z",
		"createdAt":   "2026-03-01T11:55:00Z",
		"updatedAt":   "2026-03-01T11:55:00Z",
		"timestamp":   1740830400,
		"attempt":     1,
		"maxAttempts": 3,
	})
}

// readWSMessage reads a JSON message from the WebSocket connection with a timeout.
func readWSMessage(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	require.NoError(t, err)
	var msg map[string]any
	require.NoError(t, json.Unmarshal(raw, &msg))
	return msg
}

func TestResult_Constructors(t *testing.T) {
	ack := Ack()
	assert.Equal(t, "ack", ack.kind)
	assert.Nil(t, ack.err)
	assert.Equal(t, 0, ack.timeout)

	accept := Accept(30)
	assert.Equal(t, "accept", accept.kind)
	assert.Equal(t, 30, accept.timeout)
	assert.Nil(t, accept.err)

	nackErr := fmt.Errorf("processing failed")
	nack := Nack(nackErr)
	assert.Equal(t, "nack", nack.kind)
	assert.Equal(t, nackErr, nack.err)

	nackNil := Nack(nil)
	assert.Equal(t, "nack", nackNil.kind)
	assert.Nil(t, nackNil.err)
}

func TestFetchTicket(t *testing.T) {
	server, _ := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	ticket, err := fetchTicket(context.Background(), client)
	require.NoError(t, err)
	assert.Equal(t, "test-ticket", ticket.Data.Ticket)
	assert.Contains(t, ticket.Data.URL, "/ws")
}

func TestFetchTicket_AuthError(t *testing.T) {
	server, _ := testWSServer(t, true)
	client := testListenClient(t, server.URL)

	_, err := fetchTicket(context.Background(), client)
	require.Error(t, err)

	var authErr *AuthenticationError
	assert.True(t, errors.As(err, &authErr))
}

func TestListen_HookDeliveryAck(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	delivered := make(chan *Delivery, 1)
	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		delivered <- d
		return Ack()
	})
	require.NoError(t, err)
	defer listener.Close()

	// Wait for connection.
	conn := <-conns

	// Send a hook.
	sendHookMessage(t, conn, "hook-1")

	// Wait for delivery.
	select {
	case d := <-delivered:
		assert.Equal(t, "hook-1", d.HookID)
		assert.Equal(t, "/webhooks/test", d.Path)
		assert.NotNil(t, d.WS)
		assert.Equal(t, int32(1), d.WS.Attempt)
		assert.Equal(t, int32(3), d.WS.MaxAttempts)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for delivery")
	}

	// Read ack message from server side.
	msg := readWSMessage(t, conn)
	assert.Equal(t, "ack", msg["type"])
	assert.Equal(t, "hook-1", msg["hookId"])
}

func TestListen_HandlerReturnsAccept(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Accept(60)
	})
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns
	sendHookMessage(t, conn, "hook-accept")

	msg := readWSMessage(t, conn)
	assert.Equal(t, "accept", msg["type"])
	assert.Equal(t, "hook-accept", msg["hookId"])
	assert.Equal(t, float64(60), msg["timeout"])
}

func TestListen_HandlerReturnsNack(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Nack(fmt.Errorf("something went wrong"))
	})
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns
	sendHookMessage(t, conn, "hook-nack")

	msg := readWSMessage(t, conn)
	assert.Equal(t, "nack", msg["type"])
	assert.Equal(t, "hook-nack", msg["hookId"])
	assert.Equal(t, "something went wrong", msg["error"])
}

func TestListen_HandlerPanicSendsNack(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		panic("unexpected error")
	})
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns
	sendHookMessage(t, conn, "hook-panic")

	msg := readWSMessage(t, conn)
	assert.Equal(t, "nack", msg["type"])
	assert.Equal(t, "hook-panic", msg["hookId"])
	assert.Contains(t, msg["error"], "handler panic")
}

func TestListen_ReconnectOnClose(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	reconnecting := make(chan int, 4)
	connected := make(chan ConnectionInfo, 4)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Ack()
	},
		OnConnected(func(info ConnectionInfo) { connected <- info }),
		OnReconnecting(func(attempt int) { reconnecting <- attempt }),
	)
	require.NoError(t, err)
	defer listener.Close()

	// First connection.
	conn1 := <-conns
	<-connected

	// Close the first connection to trigger reconnect.
	conn1.Close()

	// Should get a reconnection attempt.
	select {
	case attempt := <-reconnecting:
		assert.Equal(t, 1, attempt)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reconnect attempt")
	}

	// Second connection.
	conn2 := <-conns
	select {
	case info := <-connected:
		assert.Equal(t, "conn_test", info.ConnectionID)
		assert.Equal(t, "proj_test", info.ProjectID)
		assert.Equal(t, "Test Project", info.ProjectName)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for second connection")
	}

	// Verify second connection works.
	sendHookMessage(t, conn2, "hook-after-reconnect")
	msg := readWSMessage(t, conn2)
	assert.Equal(t, "ack", msg["type"])
	assert.Equal(t, "hook-after-reconnect", msg["hookId"])
}

func TestListen_AuthError_NoReconnect(t *testing.T) {
	server, _ := testWSServer(t, true)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Ack()
	})
	require.NoError(t, err)

	err = listener.Wait()
	require.Error(t, err)

	var wsErr *WebSocketError
	assert.True(t, errors.As(err, &wsErr))
	assert.Contains(t, wsErr.Error(), "authentication failed")
}

func TestListen_MaxConcurrency(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	var running atomic.Int32
	var maxRunning atomic.Int32
	done := make(chan struct{})

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		cur := running.Add(1)
		for {
			old := maxRunning.Load()
			if cur <= old || maxRunning.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return Ack()
	}, WithMaxConcurrency(2))
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns

	// Send 4 hooks.
	for i := 0; i < 4; i++ {
		sendHookMessage(t, conn, fmt.Sprintf("hook-conc-%d", i))
	}

	// Read all ack responses.
	go func() {
		for i := 0; i < 4; i++ {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ack responses")
	}

	assert.LessOrEqual(t, maxRunning.Load(), int32(2))
}

func TestListen_PingPong(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Ack()
	})
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns

	// Send ping.
	conn.WriteJSON(map[string]string{"type": "ping"})

	// Read pong.
	msg := readWSMessage(t, conn)
	assert.Equal(t, "pong", msg["type"])
}

func TestListen_CloseWait(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Ack()
	})
	require.NoError(t, err)

	conn := <-conns
	_ = conn

	// Close the listener.
	listener.Close()

	// Wait should return quickly.
	done := make(chan error, 1)
	go func() { done <- listener.Wait() }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listener to exit")
	}
}

func TestListen_ContextCancellation(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())

	listener, err := client.Hooks.Listen(ctx, func(ctx context.Context, d *Delivery) Result {
		return Ack()
	})
	require.NoError(t, err)

	conn := <-conns
	_ = conn

	cancel()

	done := make(chan error, 1)
	go func() { done <- listener.Wait() }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for listener to exit")
	}
}

func TestStream_ReceiveAndAck(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	stream, err := client.Hooks.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	conn := <-conns

	sendHookMessage(t, conn, "hook-stream-1")

	// Read from deliveries channel.
	select {
	case d := <-stream.Deliveries():
		assert.Equal(t, "hook-stream-1", d.HookID)
		assert.NotNil(t, d.WS)

		// Explicitly ack.
		err := stream.Ack(d.HookID)
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for delivery")
	}

	// Read ack message on server side.
	msg := readWSMessage(t, conn)
	assert.Equal(t, "ack", msg["type"])
	assert.Equal(t, "hook-stream-1", msg["hookId"])
}

func TestStream_AcceptAndNack(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	stream, err := client.Hooks.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	conn := <-conns

	// Test accept.
	sendHookMessage(t, conn, "hook-stream-accept")
	d := <-stream.Deliveries()
	err = stream.Accept(d.HookID, 120)
	require.NoError(t, err)

	msg := readWSMessage(t, conn)
	assert.Equal(t, "accept", msg["type"])
	assert.Equal(t, "hook-stream-accept", msg["hookId"])
	assert.Equal(t, float64(120), msg["timeout"])

	// Test nack.
	sendHookMessage(t, conn, "hook-stream-nack")
	d = <-stream.Deliveries()
	err = stream.Nack(d.HookID, fmt.Errorf("bad data"))
	require.NoError(t, err)

	msg = readWSMessage(t, conn)
	assert.Equal(t, "nack", msg["type"])
	assert.Equal(t, "hook-stream-nack", msg["hookId"])
	assert.Equal(t, "bad data", msg["error"])
}

func TestStream_CloseWait(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	stream, err := client.Hooks.Stream(context.Background())
	require.NoError(t, err)

	conn := <-conns
	_ = conn

	stream.Close()

	done := make(chan error, 1)
	go func() { done <- stream.Wait() }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream to exit")
	}
}

func TestStream_AuthError(t *testing.T) {
	server, _ := testWSServer(t, true)
	client := testListenClient(t, server.URL)

	stream, err := client.Hooks.Stream(context.Background())
	require.NoError(t, err)

	err = stream.Wait()
	require.Error(t, err)

	var wsErr *WebSocketError
	assert.True(t, errors.As(err, &wsErr))
	assert.Contains(t, wsErr.Error(), "authentication failed")
}

func TestHookMessageToDelivery(t *testing.T) {
	msg := &hookMessage{
		Type:        "hook",
		ID:          "hook-conv",
		Path:        "/webhooks/user",
		Data:        json.RawMessage(`{"event":"test"}`),
		PostAt:      "2026-03-01T12:00:00Z",
		PostedAt:    "2026-03-01T12:00:01Z",
		CreatedAt:   "2026-03-01T11:55:00Z",
		UpdatedAt:   "2026-03-01T11:55:00Z",
		Timestamp:   1740830400,
		Attempt:     2,
		MaxAttempts: 5,
		AckURL:      "https://api.posthook.io/ack/tok",
		NackURL:     "https://api.posthook.io/nack/tok",
		ForwardRequest: &wsForwardRequest{
			Body:              `{"userId":"123"}`,
			Signature:         "v1,abc",
			Authorization:     "Bearer token",
			PosthookId:        "hook-conv",
			PosthookTimestamp: "1740830400",
			PosthookSignature: "v1,def",
		},
	}

	d := hookMessageToDelivery(msg)
	assert.Equal(t, "hook-conv", d.HookID)
	assert.Equal(t, "/webhooks/user", d.Path)
	assert.Equal(t, int64(1740830400), d.Timestamp)
	assert.Equal(t, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), d.PostAt)
	assert.Equal(t, time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC), d.PostedAt)
	assert.Equal(t, "https://api.posthook.io/ack/tok", d.AckURL)
	assert.Equal(t, "https://api.posthook.io/nack/tok", d.NackURL)

	require.NotNil(t, d.WS)
	assert.Equal(t, int32(2), d.WS.Attempt)
	assert.Equal(t, int32(5), d.WS.MaxAttempts)

	require.NotNil(t, d.WS.ForwardRequest)
	assert.Equal(t, `{"userId":"123"}`, d.WS.ForwardRequest.Body)
	assert.Equal(t, "v1,abc", d.WS.ForwardRequest.Signature)
	assert.Equal(t, "Bearer token", d.WS.ForwardRequest.Authorization)
	assert.Equal(t, "hook-conv", d.WS.ForwardRequest.PosthookId)
}

func TestHookMessageToDelivery_NoForwardRequest(t *testing.T) {
	msg := &hookMessage{
		ID:          "hook-simple",
		Path:        "/test",
		Data:        json.RawMessage(`{}`),
		Attempt:     1,
		MaxAttempts: 3,
	}

	d := hookMessageToDelivery(msg)
	assert.Equal(t, "hook-simple", d.HookID)
	require.NotNil(t, d.WS)
	assert.Equal(t, int32(1), d.WS.Attempt)
	assert.Nil(t, d.WS.ForwardRequest)
}

func TestReconnectDelay(t *testing.T) {
	assert.Equal(t, 1*time.Second, reconnectDelay(0))
	assert.Equal(t, 2*time.Second, reconnectDelay(1))
	assert.Equal(t, 4*time.Second, reconnectDelay(2))
	assert.Equal(t, 8*time.Second, reconnectDelay(3))
	assert.Equal(t, 16*time.Second, reconnectDelay(4))
	assert.Equal(t, 30*time.Second, reconnectDelay(5)) // capped
	assert.Equal(t, 30*time.Second, reconnectDelay(10)) // still capped
}

func TestIsAuthError(t *testing.T) {
	assert.True(t, isAuthError(newError(401, "unauthorized")))
	assert.True(t, isAuthError(newError(403, "forbidden")))
	assert.False(t, isAuthError(newError(500, "server error")))
	assert.False(t, isAuthError(newError(404, "not found")))
	assert.False(t, isAuthError(nil))
}

func TestWebSocketError(t *testing.T) {
	err := &WebSocketError{Err: &Error{Message: "connection lost"}}
	assert.Contains(t, err.Error(), "connection lost")

	var base *Error
	assert.True(t, errors.As(err, &base))
	assert.Equal(t, "connection lost", base.Message)
}

func TestListen_OnConnectedCallback(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	connInfo := make(chan ConnectionInfo, 1)
	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Ack()
	}, OnConnected(func(info ConnectionInfo) {
		connInfo <- info
	}))
	require.NoError(t, err)
	defer listener.Close()

	<-conns

	select {
	case info := <-connInfo:
		assert.Equal(t, "conn_test", info.ConnectionID)
		assert.Equal(t, "proj_test", info.ProjectID)
		assert.Equal(t, "Test Project", info.ProjectName)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for connected callback")
	}
}

func TestListen_ClosingMessageTriggersReconnect(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	reconnected := make(chan struct{}, 1)
	connectCount := atomic.Int32{}

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Ack()
	}, OnConnected(func(info ConnectionInfo) {
		if connectCount.Add(1) == 2 {
			reconnected <- struct{}{}
		}
	}))
	require.NoError(t, err)
	defer listener.Close()

	// First connection.
	conn1 := <-conns

	// Send closing message.
	conn1.WriteJSON(map[string]string{"type": "closing"})

	// Should reconnect.
	select {
	case <-reconnected:
		// Success.
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reconnection after closing message")
	}

	// Second connection should work.
	conn2 := <-conns
	_ = conn2
}

func TestListen_AuthCloseCodeAbortsWithoutReconnect(t *testing.T) {
	for _, code := range []int{4001, 4003} {
		t.Run(fmt.Sprintf("code_%d", code), func(t *testing.T) {
			server, conns := testWSServer(t, false)
			client := testListenClient(t, server.URL)

			reconnectAttempted := atomic.Bool{}

			listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
				return Ack()
			},
				OnReconnecting(func(attempt int) {
					reconnectAttempted.Store(true)
				}),
			)
			require.NoError(t, err)

			conn := <-conns

			// Server closes with auth error code
			conn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(code, "auth error"),
			)

			// Listener should exit with an error, not reconnect
			done := make(chan error, 1)
			go func() { done <- listener.Wait() }()

			select {
			case err := <-done:
				require.Error(t, err)
				var wsErr *WebSocketError
				assert.True(t, errors.As(err, &wsErr))
				assert.Contains(t, wsErr.Error(), "auth error")
				assert.False(t, reconnectAttempted.Load(), "should not attempt reconnection on auth close code")
			case <-time.After(5 * time.Second):
				t.Fatal("timeout waiting for listener to abort")
			}
		})
	}
}

func TestStream_AuthCloseCodeAbortsWithoutReconnect(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	reconnectAttempted := atomic.Bool{}

	stream, err := client.Hooks.Stream(context.Background(),
		OnReconnecting(func(attempt int) {
			reconnectAttempted.Store(true)
		}),
	)
	require.NoError(t, err)

	conn := <-conns

	// Server closes with auth error code
	conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(4001, "auth error"),
	)

	// Deliveries channel should close
	delivery, ok := <-stream.Deliveries()
	assert.Nil(t, delivery)
	assert.False(t, ok, "deliveries channel should be closed")
	assert.False(t, reconnectAttempted.Load(), "should not attempt reconnection on auth close code")

	err = stream.Wait()
	require.Error(t, err)
	var wsErr *WebSocketError
	assert.True(t, errors.As(err, &wsErr))
}

func TestListen_NackNilError(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		return Nack(nil)
	})
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns
	sendHookMessage(t, conn, "hook-nack-nil")

	msg := readWSMessage(t, conn)
	assert.Equal(t, "nack", msg["type"])
	assert.Equal(t, "hook-nack-nil", msg["hookId"])
	// Error field should not be present when err is nil.
	_, hasError := msg["error"]
	assert.False(t, hasError)
}

func TestStream_OnConnectedCallback(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	connInfo := make(chan ConnectionInfo, 1)
	stream, err := client.Hooks.Stream(context.Background(),
		OnConnected(func(info ConnectionInfo) {
			connInfo <- info
		}),
	)
	require.NoError(t, err)
	defer stream.Close()

	<-conns

	select {
	case info := <-connInfo:
		assert.Equal(t, "conn_test", info.ConnectionID)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for connected callback")
	}
}

func TestListen_MultipleHooksSequential(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	var deliveryOrder []string
	var mu sync.Mutex
	allDone := make(chan struct{})

	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		mu.Lock()
		deliveryOrder = append(deliveryOrder, d.HookID)
		if len(deliveryOrder) == 3 {
			close(allDone)
		}
		mu.Unlock()
		return Ack()
	}, WithMaxConcurrency(1))
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns

	sendHookMessage(t, conn, "hook-seq-1")
	sendHookMessage(t, conn, "hook-seq-2")
	sendHookMessage(t, conn, "hook-seq-3")

	select {
	case <-allDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for all deliveries")
	}

	mu.Lock()
	defer mu.Unlock()

	// With maxConcurrency=1, hooks should be processed in order.
	assert.Equal(t, []string{"hook-seq-1", "hook-seq-2", "hook-seq-3"}, deliveryOrder)

	// Read all acks.
	for i := 0; i < 3; i++ {
		msg := readWSMessage(t, conn)
		assert.Equal(t, "ack", msg["type"])
	}
}

func TestHTTPHandler_Ack(t *testing.T) {
	key := "test-signing-key"
	svc := &SignaturesService{signingKey: key}

	handler := svc.HTTPHandler(func(ctx context.Context, d *Delivery) Result {
		return Ack()
	})

	timestamp := time.Now().Unix()
	body := []byte(`{"id":"hook-http","path":"/webhooks/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{"key":"value"},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-http", timestamp, sig)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/test", strings.NewReader(string(body)))
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Set(k, vv)
		}
	}

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"ok":true`)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestHTTPHandler_Accept(t *testing.T) {
	key := "test-signing-key"
	svc := &SignaturesService{signingKey: key}

	handler := svc.HTTPHandler(func(ctx context.Context, d *Delivery) Result {
		return Accept(60)
	})

	timestamp := time.Now().Unix()
	body := []byte(`{"id":"hook-http","path":"/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-http", timestamp, sig)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(body)))
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Set(k, vv)
		}
	}

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), `"ok":true`)
}

func TestHTTPHandler_Nack(t *testing.T) {
	key := "test-signing-key"
	svc := &SignaturesService{signingKey: key}

	handler := svc.HTTPHandler(func(ctx context.Context, d *Delivery) Result {
		return Nack(fmt.Errorf("processing failed"))
	})

	timestamp := time.Now().Unix()
	body := []byte(`{"id":"hook-http","path":"/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-http", timestamp, sig)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(body)))
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Set(k, vv)
		}
	}

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "processing failed")
}

func TestHTTPHandler_InvalidSignature(t *testing.T) {
	key := "test-signing-key"
	svc := &SignaturesService{signingKey: key}

	handler := svc.HTTPHandler(func(ctx context.Context, d *Delivery) Result {
		t.Fatal("handler should not be called")
		return Ack()
	})

	timestamp := time.Now().Unix()
	body := []byte(`{"id":"hook-http","path":"/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	// Use wrong key for signature.
	sig := computeTestSignature("wrong-key", timestamp, body)
	headers := makeHeaders("hook-http", timestamp, sig)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(body)))
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Set(k, vv)
		}
	}

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHTTPHandler_HandlerPanic(t *testing.T) {
	key := "test-signing-key"
	svc := &SignaturesService{signingKey: key}

	handler := svc.HTTPHandler(func(ctx context.Context, d *Delivery) Result {
		panic("unexpected")
	})

	timestamp := time.Now().Unix()
	body := []byte(`{"id":"hook-http","path":"/test","postAt":"2026-02-22T15:00:00Z","postedAt":"2026-02-22T15:00:01Z","data":{},"createdAt":"2026-02-22T14:55:00Z","updatedAt":"2026-02-22T14:55:00Z"}`)
	sig := computeTestSignature(key, timestamp, body)
	headers := makeHeaders("hook-http", timestamp, sig)

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(body)))
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Set(k, vv)
		}
	}

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "handler panic")
}

func TestListen_HookWithForwardRequest(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	delivered := make(chan *Delivery, 1)
	listener, err := client.Hooks.Listen(context.Background(), func(ctx context.Context, d *Delivery) Result {
		delivered <- d
		return Ack()
	})
	require.NoError(t, err)
	defer listener.Close()

	conn := <-conns

	// Send a hook with forwardRequest.
	conn.WriteJSON(map[string]any{
		"type":        "hook",
		"id":          "hook-fwd",
		"path":        "/webhooks/test",
		"data":        map[string]any{"key": "value"},
		"postAt":      "2026-03-01T12:00:00Z",
		"postedAt":    "2026-03-01T12:00:01Z",
		"createdAt":   "2026-03-01T11:55:00Z",
		"updatedAt":   "2026-03-01T11:55:00Z",
		"timestamp":   1740830400,
		"attempt":     1,
		"maxAttempts": 3,
		"forwardRequest": map[string]any{
			"body":              `{"userId":"123"}`,
			"signature":         "v1,abc",
			"authorization":     "Bearer tok",
			"posthookId":        "hook-fwd",
			"posthookTimestamp": "1740830400",
			"posthookSignature": "v1,sig",
		},
	})

	select {
	case d := <-delivered:
		assert.Equal(t, "hook-fwd", d.HookID)
		require.NotNil(t, d.WS)
		require.NotNil(t, d.WS.ForwardRequest)
		assert.Equal(t, `{"userId":"123"}`, d.WS.ForwardRequest.Body)
		assert.Equal(t, "v1,abc", d.WS.ForwardRequest.Signature)
		assert.Equal(t, "Bearer tok", d.WS.ForwardRequest.Authorization)
		assert.Equal(t, "hook-fwd", d.WS.ForwardRequest.PosthookId)
		assert.Equal(t, "1740830400", d.WS.ForwardRequest.PosthookTimestamp)
		assert.Equal(t, "v1,sig", d.WS.ForwardRequest.PosthookSignature)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for delivery")
	}

	msg := readWSMessage(t, conn)
	assert.Equal(t, "ack", msg["type"])
}

func TestStream_NackNilError(t *testing.T) {
	server, conns := testWSServer(t, false)
	client := testListenClient(t, server.URL)

	stream, err := client.Hooks.Stream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	conn := <-conns
	sendHookMessage(t, conn, "hook-stream-nack-nil")

	d := <-stream.Deliveries()
	err = stream.Nack(d.HookID, nil)
	require.NoError(t, err)

	msg := readWSMessage(t, conn)
	assert.Equal(t, "nack", msg["type"])
	assert.Equal(t, "hook-stream-nack-nil", msg["hookId"])
	_, hasError := msg["error"]
	assert.False(t, hasError)
}

func TestWithMaxConcurrency_ZeroUsesDefault(t *testing.T) {
	cfg := listenConfig{maxConcurrency: 1}
	WithMaxConcurrency(0)(&cfg)
	assert.Equal(t, 1, cfg.maxConcurrency) // unchanged
}

func TestWithMaxConcurrency_Positive(t *testing.T) {
	cfg := listenConfig{maxConcurrency: 1}
	WithMaxConcurrency(5)(&cfg)
	assert.Equal(t, 5, cfg.maxConcurrency)
}

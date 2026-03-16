package posthook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Result represents the outcome of processing a webhook delivery.
type Result struct {
	kind    string // "ack", "accept", "nack"
	timeout int
	err     error
}

// Ack returns a Result indicating the delivery was processed successfully.
func Ack() Result { return Result{kind: "ack"} }

// Accept returns a Result indicating the delivery was accepted for async
// processing. The timeout (in seconds) tells the server how long to wait
// before considering the delivery failed.
func Accept(timeout int) Result { return Result{kind: "accept", timeout: timeout} }

// Nack returns a Result indicating the delivery failed. The error, if
// non-nil, is sent to the server as the failure reason.
func Nack(err error) Result { return Result{kind: "nack", err: err} }

// ListenOption configures a Listener or Stream.
type ListenOption func(*listenConfig)

type listenConfig struct {
	maxConcurrency int
	onConnected    func(ConnectionInfo)
	onDisconnected func(error)
	onReconnecting func(attempt int)
}

// WithMaxConcurrency sets the maximum number of concurrent handler goroutines.
// Defaults to unlimited. Set to 1 for sequential processing.
func WithMaxConcurrency(n int) ListenOption {
	return func(c *listenConfig) {
		if n > 0 {
			c.maxConcurrency = n
		}
	}
}

// OnConnected registers a callback that fires after a WebSocket connection
// is established and the server sends the "connected" message.
func OnConnected(fn func(ConnectionInfo)) ListenOption {
	return func(c *listenConfig) { c.onConnected = fn }
}

// OnDisconnected registers a callback that fires when the WebSocket
// connection is lost.
func OnDisconnected(fn func(error)) ListenOption {
	return func(c *listenConfig) { c.onDisconnected = fn }
}

// OnReconnecting registers a callback that fires before each reconnection
// attempt. The attempt parameter is 1-indexed.
func OnReconnecting(fn func(attempt int)) ListenOption {
	return func(c *listenConfig) { c.onReconnecting = fn }
}

// ConnectionInfo contains metadata about the WebSocket connection.
type ConnectionInfo struct {
	ConnectionID string
	ProjectID    string
	ProjectName  string
}

// Listener receives webhook deliveries via WebSocket and dispatches them
// to a handler function. It automatically reconnects on disconnection.
type Listener struct {
	client  *Client
	handler func(context.Context, *Delivery) Result
	config  listenConfig
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

// Close cancels the listener. After Close returns, no new deliveries will
// be dispatched. Call Wait to block until the listener loop exits.
func (l *Listener) Close() { l.cancel() }

// Wait blocks until the listener loop exits and returns any terminal error.
// A nil error means the context was cancelled or Close was called.
func (l *Listener) Wait() error { <-l.done; return l.err }

// Stream provides a channel-based interface for receiving webhook deliveries
// via WebSocket. The caller reads from Deliveries() and explicitly acks,
// accepts, or nacks each delivery.
type Stream struct {
	client     *Client
	config     listenConfig
	cancel     context.CancelFunc
	done       chan struct{}
	deliveries chan *Delivery
	ws         *websocket.Conn
	mu         sync.Mutex
	err        error
}

// Deliveries returns a read-only channel of incoming webhook deliveries.
// The channel is closed when the stream exits.
func (s *Stream) Deliveries() <-chan *Delivery { return s.deliveries }

// Ack acknowledges a delivery by hook ID over the WebSocket connection.
func (s *Stream) Ack(hookID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ws == nil {
		return fmt.Errorf("posthook: not connected")
	}
	return sendAckMsg(s.ws, hookID)
}

// Accept accepts a delivery for async processing by hook ID.
func (s *Stream) Accept(hookID string, timeout int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ws == nil {
		return fmt.Errorf("posthook: not connected")
	}
	return sendAcceptMsg(s.ws, hookID, timeout)
}

// Nack rejects a delivery by hook ID. If err is non-nil, its message is
// sent to the server as the failure reason.
func (s *Stream) Nack(hookID string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ws == nil {
		return fmt.Errorf("posthook: not connected")
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	return sendNackMsg(s.ws, hookID, errMsg)
}

// Close cancels the stream. After Close returns, the Deliveries channel
// will eventually be closed. Call Wait to block until the stream loop exits.
func (s *Stream) Close() { s.cancel() }

// Wait blocks until the stream loop exits and returns any terminal error.
func (s *Stream) Wait() error { <-s.done; return s.err }

// Listen starts receiving webhook deliveries via WebSocket. Each delivery is
// passed to the handler, which must return a Result (Ack, Accept, or Nack).
// The listener automatically reconnects on disconnection with exponential
// backoff. It stops when the context is cancelled or Close is called.
func (h *HooksService) Listen(ctx context.Context, handler func(context.Context, *Delivery) Result, opts ...ListenOption) (*Listener, error) {
	cfg := listenConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	ctx, cancel := context.WithCancel(ctx)
	l := &Listener{
		client:  h.client,
		handler: handler,
		config:  cfg,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	go l.run(ctx)
	return l, nil
}

// Stream starts receiving webhook deliveries via WebSocket and sends them to
// a channel. The caller must explicitly ack, accept, or nack each delivery.
func (h *HooksService) Stream(ctx context.Context, opts ...ListenOption) (*Stream, error) {
	cfg := listenConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		client:     h.client,
		config:     cfg,
		cancel:     cancel,
		done:       make(chan struct{}),
		deliveries: make(chan *Delivery, 64),
	}

	go s.run(ctx)
	return s, nil
}

// ticketResponse is the API response for POST /v1/ws/ticket.
type ticketResponse struct {
	Data struct {
		Ticket    string `json:"ticket"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"data"`
}

// fetchTicket requests a WebSocket connection ticket from the API.
func fetchTicket(ctx context.Context, client *Client) (*ticketResponse, error) {
	req, err := client.newRequest(http.MethodPost, "/v1/ws/ticket", nil)
	if err != nil {
		return nil, fmt.Errorf("posthook: failed to create ticket request: %w", err)
	}

	body, _, err := client.execute(ctx, req)
	if err != nil {
		return nil, err
	}

	var ticket ticketResponse
	if err := json.Unmarshal(body, &ticket); err != nil {
		return nil, fmt.Errorf("posthook: failed to parse ticket response: %w", err)
	}

	return &ticket, nil
}

// connectedMessage is the "connected" message from the server.
type connectedMessage struct {
	Type         string `json:"type"`
	ConnectionID string `json:"connectionId"`
	ProjectID    string `json:"projectId"`
	ProjectName  string `json:"projectName"`
}

// hookMessage is a "hook" message from the server.
type hookMessage struct {
	Type           string            `json:"type"`
	ID             string            `json:"id"`
	Path           string            `json:"path"`
	Data           json.RawMessage   `json:"data"`
	PostAt         string            `json:"postAt"`
	PostedAt       string            `json:"postedAt"`
	CreatedAt      string            `json:"createdAt"`
	UpdatedAt      string            `json:"updatedAt"`
	Timestamp      int64             `json:"timestamp"`
	Attempt        int32             `json:"attempt"`
	MaxAttempts    int32             `json:"maxAttempts"`
	AckURL         string            `json:"ackUrl"`
	NackURL        string            `json:"nackUrl"`
	ForwardRequest *wsForwardRequest `json:"forwardRequest"`
}

// wsForwardRequest contains forwarded HTTP request metadata.
type wsForwardRequest struct {
	Body              string `json:"body"`
	Signature         string `json:"signature"`
	Authorization     string `json:"authorization"`
	PosthookId        string `json:"posthookId"`
	PosthookTimestamp string `json:"posthookTimestamp"`
	PosthookSignature string `json:"posthookSignature"`
}

// wsMessage is a generic incoming WebSocket message used to determine type.
type wsMessage struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

const (
	maxReconnectAttempts = 10
	maxBackoff           = 30 * time.Second
	inactivityTimeout    = 45 * time.Second
)

// reconnectDelay returns the backoff delay for the given attempt (0-indexed).
func reconnectDelay(attempt int) time.Duration {
	return min(time.Duration(math.Pow(2, float64(attempt)))*time.Second, maxBackoff)
}

// isAuthError returns true if the error is a 401 or 403 API error.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *AuthenticationError
	var forbErr *ForbiddenError
	if errors.As(err, &authErr) || errors.As(err, &forbErr) {
		return true
	}
	return false
}

func hookMessageToDelivery(msg *hookMessage) *Delivery {
	postAt, _ := time.Parse(time.RFC3339, msg.PostAt)
	postedAt, _ := time.Parse(time.RFC3339, msg.PostedAt)
	createdAt, _ := time.Parse(time.RFC3339, msg.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339, msg.UpdatedAt)

	d := &Delivery{
		HookID:    msg.ID,
		Timestamp: msg.Timestamp,
		Path:      msg.Path,
		Data:      msg.Data,
		PostAt:    postAt,
		PostedAt:  postedAt,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		AckURL:    msg.AckURL,
		NackURL:   msg.NackURL,
		WS: &WebSocketMeta{
			Attempt:     msg.Attempt,
			MaxAttempts: msg.MaxAttempts,
		},
	}

	if msg.ForwardRequest != nil {
		d.WS.ForwardRequest = &ForwardRequest{
			Body:              msg.ForwardRequest.Body,
			Signature:         msg.ForwardRequest.Signature,
			Authorization:     msg.ForwardRequest.Authorization,
			PosthookId:        msg.ForwardRequest.PosthookId,
			PosthookTimestamp: msg.ForwardRequest.PosthookTimestamp,
			PosthookSignature: msg.ForwardRequest.PosthookSignature,
		}
	}

	return d
}

// sendAckMsg sends an ack message over the WebSocket.
func sendAckMsg(conn *websocket.Conn, hookID string) error {
	return conn.WriteJSON(map[string]string{"type": "ack", "hookId": hookID})
}

// sendAcceptMsg sends an accept message over the WebSocket.
func sendAcceptMsg(conn *websocket.Conn, hookID string, timeout int) error {
	return conn.WriteJSON(map[string]any{"type": "accept", "hookId": hookID, "timeout": timeout})
}

// sendNackMsg sends a nack message over the WebSocket.
func sendNackMsg(conn *websocket.Conn, hookID string, errMsg string) error {
	msg := map[string]string{"type": "nack", "hookId": hookID}
	if errMsg != "" {
		msg["error"] = errMsg
	}
	return conn.WriteJSON(msg)
}

// --- Shared reconnect and message loop ---

// wsCallbacks captures the per-mode differences between Listener and Stream.
// The shared wsLoop and messageLoop use these to avoid duplicating the
// reconnect logic, backoff, ticket fetch, and message-type dispatching.
type wsCallbacks struct {
	client *Client
	config *listenConfig

	// onDial is called after a successful WebSocket dial, before entering the
	// message loop. Stream uses this to set s.ws.
	onDial func(conn *websocket.Conn)

	// onClose is called after the connection is closed at the end of each
	// reconnect iteration. Stream uses this to clear s.ws.
	onClose func()

	// onHook is called for each "hook" message. It returns false if the
	// message loop should exit (e.g., context cancelled while pushing to
	// channel).
	onHook func(ctx context.Context, conn *websocket.Conn, delivery *Delivery) (cont bool)

	// writeMsg sends a JSON message on the connection. Implementations that
	// share the conn with external writers (Stream) wrap this in a mutex;
	// Listener simply calls conn.WriteJSON directly.
	writeMsg func(conn *websocket.Conn, v any) error

	// waitInflight blocks until all in-flight handler goroutines finish.
	// Only Listener uses this (for its WaitGroup); Stream returns immediately.
	waitInflight func()

	// setErr stores the terminal error.
	setErr func(err error)
}

// wsLoop is the shared reconnect loop used by both Listener and Stream.
func wsLoop(ctx context.Context, cb *wsCallbacks) {
	attempts := 0
	for {
		if ctx.Err() != nil {
			return
		}

		if attempts > 0 {
			if attempts > maxReconnectAttempts {
				cb.setErr(&WebSocketError{Err: &Error{Message: "max reconnection attempts exceeded"}})
				return
			}

			if cb.config.onReconnecting != nil {
				cb.config.onReconnecting(attempts)
			}

			delay := reconnectDelay(attempts - 1)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}

		ticket, err := fetchTicket(ctx, cb.client)
		if err != nil {
			if isAuthError(err) {
				cb.setErr(&WebSocketError{Err: &Error{
					Message: fmt.Sprintf("authentication failed: %v", err),
				}})
				return
			}
			attempts++
			continue
		}

		dialURL := ticket.Data.URL + "?ticket=" + url.QueryEscape(ticket.Data.Ticket)
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, dialURL, nil)
		if err != nil {
			if cb.config.onDisconnected != nil {
				cb.config.onDisconnected(err)
			}
			attempts++
			continue
		}

		if cb.onDial != nil {
			cb.onDial(conn)
		}

		connectedOK, authClosed := messageLoop(ctx, conn, &attempts, cb)
		conn.Close()

		if cb.onClose != nil {
			cb.onClose()
		}

		if cb.config.onDisconnected != nil && connectedOK {
			cb.config.onDisconnected(nil)
		}

		if authClosed {
			cb.setErr(&WebSocketError{Err: &Error{
				Message: "server closed connection with auth error",
			}})
			return
		}

		if ctx.Err() != nil {
			return
		}

		attempts++
	}
}

// authCloseCodes are WebSocket close codes that indicate an authentication
// error. The server sends these when the API key is invalid or revoked.
var authCloseCodes = map[int]bool{4001: true, 4003: true}

// messageLoop reads and dispatches WebSocket messages until the connection
// drops or the context is cancelled. It returns (connectedOK, authClosed):
// connectedOK is true if a "connected" message was received, and authClosed
// is true if the server closed the connection with an auth error code.
func messageLoop(ctx context.Context, conn *websocket.Conn, attempts *int, cb *wsCallbacks) (bool, bool) {
	connectedOK := false

	// Close the connection when the context is cancelled so that
	// ReadMessage() unblocks immediately instead of waiting for the
	// inactivity timeout.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// Set initial read deadline for inactivity timeout.
	conn.SetReadDeadline(time.Now().Add(inactivityTimeout))

	for {
		if ctx.Err() != nil {
			cb.waitInflight()
			return connectedOK, false
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			cb.waitInflight()
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) && authCloseCodes[closeErr.Code] {
				return connectedOK, true
			}
			return connectedOK, false
		}

		// Reset read deadline on any message.
		conn.SetReadDeadline(time.Now().Add(inactivityTimeout))

		var base wsMessage
		if err := json.Unmarshal(raw, &base); err != nil {
			continue
		}

		switch base.Type {
		case "connected":
			var cm connectedMessage
			if err := json.Unmarshal(raw, &cm); err != nil {
				continue
			}
			connectedOK = true
			*attempts = 0
			if cb.config.onConnected != nil {
				cb.config.onConnected(ConnectionInfo{
					ConnectionID: cm.ConnectionID,
					ProjectID:    cm.ProjectID,
					ProjectName:  cm.ProjectName,
				})
			}

		case "hook":
			var hm hookMessage
			if err := json.Unmarshal(raw, &hm); err != nil {
				continue
			}
			delivery := hookMessageToDelivery(&hm)
			if !cb.onHook(ctx, conn, delivery) {
				cb.waitInflight()
				return connectedOK, false
			}

		case "ping":
			cb.writeMsg(conn, map[string]string{"type": "pong"})

		case "closing":
			cb.waitInflight()
			return connectedOK, false

		case "error":
			continue

		case "ack_timeout":
			continue
		}
	}
}

// --- Listener implementation ---

func (l *Listener) run(ctx context.Context) {
	defer close(l.done)

	var sem chan struct{}
	if l.config.maxConcurrency > 0 {
		sem = make(chan struct{}, l.config.maxConcurrency)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex

	wsLoop(ctx, &wsCallbacks{
		client: l.client,
		config: &l.config,
		setErr: func(err error) { l.err = err },
		writeMsg: func(conn *websocket.Conn, v any) error {
			return conn.WriteJSON(v)
		},
		waitInflight: func() { wg.Wait() },
		onHook: func(ctx context.Context, conn *websocket.Conn, delivery *Delivery) bool {
			wg.Add(1)
			if sem != nil {
				sem <- struct{}{}
			}
			go func() {
				defer wg.Done()
				if sem != nil {
					defer func() { <-sem }()
				}

				var result Result
				func() {
					defer func() {
						if r := recover(); r != nil {
							result = Nack(fmt.Errorf("handler panic: %v", r))
						}
					}()
					result = l.handler(ctx, delivery)
				}()

				mu.Lock()
				defer mu.Unlock()

				switch result.kind {
				case "ack":
					sendAckMsg(conn, delivery.HookID)
				case "accept":
					sendAcceptMsg(conn, delivery.HookID, result.timeout)
				case "nack":
					errMsg := ""
					if result.err != nil {
						errMsg = result.err.Error()
					}
					sendNackMsg(conn, delivery.HookID, errMsg)
				default:
					sendAckMsg(conn, delivery.HookID)
				}
			}()
			return true
		},
	})
}

// --- Stream implementation ---

func (s *Stream) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.deliveries)

	wsLoop(ctx, &wsCallbacks{
		client: s.client,
		config: &s.config,
		setErr: func(err error) { s.err = err },
		onDial: func(conn *websocket.Conn) {
			s.mu.Lock()
			s.ws = conn
			s.mu.Unlock()
		},
		onClose: func() {
			s.mu.Lock()
			s.ws = nil
			s.mu.Unlock()
		},
		writeMsg: func(conn *websocket.Conn, v any) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			return conn.WriteJSON(v)
		},
		waitInflight: func() {},
		onHook: func(ctx context.Context, conn *websocket.Conn, delivery *Delivery) bool {
			select {
			case s.deliveries <- delivery:
				return true
			case <-ctx.Done():
				return false
			}
		},
	})
}
